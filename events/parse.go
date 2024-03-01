// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package events

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type rawEvent struct {
	name    string
	pmu     uint32
	config  uint64
	config1 uint64
	config2 uint64
	period  uint64
}

func (e *rawEvent) String() string {
	return e.name
}

func (e *rawEvent) SetAttrs(attr *unix.PerfEventAttr) error {
	attr.Type = e.pmu
	attr.Config = e.config
	attr.Ext1 = e.config1
	attr.Ext2 = e.config2
	attr.Sample = e.period // Union of sample_period and sample_freq
	return nil
}

func ParseEvent(name string) (Event, error) {
	// TODO: Support raw events
	// TODO: Support modifiers
	// TODO: Support hardware breakpoint events

	pmu, params, err := parsePMUEvent(name)
	if err == errNotPMUEvent {
		// Try as a symbolic event.
		pmu = ""
		params = []eventParam{{k: name, kOnly: true}}
	} else if err != nil {
		return nil, err
	}

	rev, err := resolveEvent(name, pmu, params)
	if err != nil {
		return nil, err
	}
	return rev, nil
}

var errNotPMUEvent = errors.New("not a PMU format event")

// parsePMUEvent parses symbolic PMU event strings in the form pmu/k=v,.../
func parsePMUEvent(name string) (pmu string, params []eventParam, err error) {
	if !(strings.Count(name, "/") == 2 && !strings.HasPrefix(name, "/") && strings.HasSuffix(name, "/")) {
		return "", nil, errNotPMUEvent
	}

	pmu, rest, _ := strings.Cut(name, "/")
	rest = strings.TrimSuffix(rest, "/")
	params, err = parseParamList(rest)
	if err != nil {
		return "", nil, fmt.Errorf("event %q: %w", name, err)
	}
	return pmu, params, nil
}

type eventParam struct {
	k     string
	v     uint64
	kOnly bool // Param may be an event name or k=1
}

// parseParamList parses a comma-separated list of k strings and k=v pairs. Lone
// keys are assumed to have value 1 and are marked as potential names.
func parseParamList(list string) ([]eventParam, error) {
	// A sole k is assumed to have a value of 1. See
	// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-bus-event_source-devices-events.
	// This is supported even in an event name, so perf has to disambiguate
	// event names and keys by looking in /sys.
	var params []eventParam
	errf := func(f string, args ...any) error {
		prefix := fmt.Sprintf("error parsing event param list %q", list)
		return fmt.Errorf("%s: "+f, append([]any{prefix}, args...)...)
	}
	for _, s := range strings.Split(list, ",") {
		k, vs, ok := strings.Cut(s, "=")
		if k == "" {
			return nil, errf("missing parameter name in %q", s)
		}
		if !ok {
			params = append(params, eventParam{k, 1, true})
			continue
		}
		// The value can be decimal, hex, or octal.
		v, err := strconv.ParseUint(vs, 0, 64)
		if err != nil {
			return nil, errf("parameter %q not a number", s)
		}
		params = append(params, eventParam{k, v, false})
	}

	return params, nil
}

type eventResolver func(pmu *pmuDesc, eventName string) (pmuEvent, error)

// errUnknownEvent is an internal error returned by eventResolver.
var errUnknownEvent = errors.New("unknown event")

var eventResolvers = []eventResolver{
	resolvePMUEvent,
	resolvePerfJsonEvent,
}

// resolveEvent resolves an event in the form pmu/param1=N,.../ or a symbolic
// event. Symbolic events will have pmu == "" and a single kOnly param.
func resolveEvent(enc string, pmu string, params []eventParam) (*rawEvent, error) {
	event := rawEvent{name: enc}

	// TODO: Could I simplify all of this by having the concept of "a thing that
	// can set fields in a rawEvent"? Then hopefully I wouldn't need
	// builtinEvent, or pmuEvent, and maybe perfJson.toPMUEvent could be more
	// direct.

	// Events with perf constants are baked in and don't necessarily appear in
	// /sys. (Though sometimes they do!) Perf will prefer this over the
	// encodings in /sys. It still allows overriding other parameters, but I
	// think that's a bug: built-in events use the built-in static PMU types,
	// and any other fields are only meaningful with the dynamic PMU types, so
	// this inevitably produces malformed events.
	if len(params) == 1 && params[0].kOnly {
		if ev, ok := resolveBuiltinEvent(pmu, params[0].k); ok {
			event.pmu = ev.pmu
			event.config = ev.config
			return &event, nil
		}
	}

	// If we get to here for a symbolic event, then the CPU PMU is implied.
	symEvent := pmu == ""
	if pmu == "" {
		pmu = "cpu"
	}

	// Check that the PMU exists and get its type.
	desc, err := pmus.get(pmu)
	if err != nil {
		return nil, err
	}
	event.pmu = desc.pmu

	// Resolve each parameter to either an event name or a PMU format.
	var pmuEv pmuEvent
	eventNameIndex := -1
Params:
	for i, param := range params {
		if _, ok := desc.getFormat(param.k); ok {
			// Known format name. We'll fill this in later.
			continue
		}
		if param.kOnly {
			for _, r := range eventResolvers {
				pmuEv2, err := r(desc, param.k)
				if err == nil {
					// Resolved event name
					if eventNameIndex != -1 {
						return nil, fmt.Errorf("event %q: multiple events %q and %q", enc, pmuEv.name, pmuEv2.name)
					}
					pmuEv, eventNameIndex = pmuEv2, i
					continue Params
				} else if err != errUnknownEvent {
					// A "real" error.
					return nil, err
				}
			}
		}
		// We failed to resolve this parameter.
		if symEvent {
			return nil, fmt.Errorf("unknown event %q", enc)
		}
		return nil, fmt.Errorf("event %q: unknown event or parameter %q", enc, param.k)
	}

	if eventNameIndex != -1 {
		// The parameters from the named event are overridden by other
		// parameters, regardless of order.
		params = append(append(append([]eventParam(nil), pmuEv.params...), params[:eventNameIndex]...), params[eventNameIndex+1:]...)
		// TODO: Do something with pmuEv.scale and unit.
	}

	// Finally, resolve the parameters into an event.
	for _, param := range params {
		f, ok := desc.getFormat(param.k)
		if !ok {
			// This can happen if the event itself introduces unknown
			// parameters.
			return nil, fmt.Errorf("event %q: unknown parameter %q in description", enc, param.k)
		}
		if err := f.set(&event, param.v); err != nil {
			return nil, fmt.Errorf("event %q: %w", enc, err)
		}
	}

	return &event, nil
}
