// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package events

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func resolvePMUEvent(pmu *pmuDesc, eventName string, ev *rawEvent) error {
	pmuEv, ok := pmu.events[eventName]
	if !ok {
		return errUnknownEvent
	}
	for _, param := range pmuEv.params {
		f, ok := pmu.getFormat(param.k)
		if !ok {
			return fmt.Errorf("unknown parameter %q in %s description", param.k, eventName)
		}
		if err := f.set(ev, param.v); err != nil {
			return err
		}
	}
	ev.scale = pmuEv.scale
	ev.unit = pmuEv.unit
	return nil
}

// The directory and fs.FS of the event source devices. These are variables so
// they can be stubbed by tests.
var (
	pmuDir = "/sys/bus/event_source/devices"
	pmuFS  = os.DirFS(pmuDir)
)

type pmuDesc struct {
	pmu    uint32
	format map[string]pmuFormat // Keyed by symbolic field name
	events map[string]pmuEvent  // Keyed by event name
}

type pmuFormat struct {
	name  string
	field func(*rawEvent) *uint64
	bits  []formatBitRange
}

type formatBitRange struct {
	shift int
	nBits int
}

var formatAllBits = []formatBitRange{{0, 64}}

type pmuEvent struct {
	name   string
	params []eventParam
	scale  float64
	unit   string
}

func fieldConfig(e *rawEvent) *uint64  { return &e.config }
func fieldConfig1(e *rawEvent) *uint64 { return &e.config1 }
func fieldConfig2(e *rawEvent) *uint64 { return &e.config2 }
func fieldPeriod(e *rawEvent) *uint64  { return &e.period }

// getFormat returns the pmuFormat for the given parameter in a PMU event
// description. E.g., in "cpu/config=42,edge/", "config" and "edge" would be
// mapped to formats using this method on the "cpu" PMU.
func (d *pmuDesc) getFormat(param string) (pmuFormat, bool) {
	// TODO: Perf also supports config3,name,percore,metric-id
	switch param {
	case "config":
		return pmuFormat{param, fieldConfig, formatAllBits}, true
	case "config1":
		return pmuFormat{param, fieldConfig1, formatAllBits}, true
	case "config2":
		return pmuFormat{param, fieldConfig2, formatAllBits}, true
	case "period":
		return pmuFormat{param, fieldPeriod, formatAllBits}, true
	}
	f, ok := d.format[param]
	return f, ok
}

// set sets the appropriate field of e to val.
func (f pmuFormat) set(e *rawEvent, val uint64) error {
	field := f.field(e)
	totalBits := 0
	x := val
	for _, bits := range f.bits {
		totalBits += bits.nBits
		max := (uint64(1) << bits.nBits) - 1
		*field &^= max << bits.shift
		*field |= (x & max) << bits.shift
		x >>= bits.nBits
	}
	if x != 0 {
		// We didn't consume all the bits, so the value is of range.
		max := (uint64(1) << totalBits) - 1
		return fmt.Errorf("parameter %s=%d not in range 0-%d", f.name, val, max)
	}
	return nil
}

// TODO: Look for a <pmu>/alias file.

// pmus is a onceMap containing descriptions for each PMU type.
var pmus = newOnceMap(func(pmu string) (*pmuDesc, error) {
	var desc pmuDesc

	// Parse the PMU type.
	path := filepath.Join(pmu, "type")
	typStr, err := fs.ReadFile(pmuFS, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("unknown PMU %q", pmu)
	} else if err != nil {
		return nil, fmt.Errorf("unknown PMU %q: %w", pmu, err)
	}
	typStr = bytes.TrimRight(typStr, "\n")
	num, err := strconv.ParseUint(string(typStr), 0, 32)
	if err != nil {
		return nil, fmt.Errorf("error parsing PMU %q type %q: %w", pmu, string(typStr), err)
	}
	desc.pmu = uint32(num)

	// Parse format.
	desc.format = make(map[string]pmuFormat)
	err = pmuForEachFile(filepath.Join(pmu, "format"), func(name string, data string) error {
		format, err := pmuParseFormat(data)
		if err != nil {
			return err
		}
		format.name = name
		desc.format[name] = format
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Parse events. See https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-bus-event_source-devices-events
	desc.events = make(map[string]pmuEvent)
	err = pmuForEachFile(filepath.Join(pmu, "events"), func(name string, data string) error {
		data = strings.TrimRight(data, "\n")

		switch {
		default:
			params, err := parseParamList(data)
			if err != nil {
				return err
			}
			desc.events[name] = pmuEvent{name: name, params: params, scale: 1.0, unit: ""}

		case strings.HasSuffix(name, ".scale"):
			name = strings.TrimSuffix(name, ".scale")
			if ev, ok := desc.events[name]; ok {
				s, err := strconv.ParseFloat(data, 64)
				if err != nil {
					return err
				}
				ev.scale = s
				desc.events[name] = ev
			}

		case strings.HasSuffix(name, ".unit"):
			name = strings.TrimSuffix(name, ".unit")
			if ev, ok := desc.events[name]; ok {
				ev.unit = data
				desc.events[name] = ev
			}

		case strings.Contains(name, "."):
			// Some other special file. Ignore.
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &desc, nil
})

// pmuForEachFile calls f for each file under path in the pmuFS.
func pmuForEachFile(path string, f func(name string, data string) error) error {
	ents, err := fs.ReadDir(pmuFS, path)
	if errors.Is(err, fs.ErrNotExist) {
		// Treat like an empty directory. All of the directories we use this on
		// are optional.
		return nil
	}
	if err != nil {
		return fmt.Errorf("error reading %s: %w", filepath.Join(pmuDir, path), err)
	}
	for _, ent := range ents {
		entPath := filepath.Join(path, ent.Name())
		b, err := fs.ReadFile(pmuFS, entPath)
		if err != nil {
			return fmt.Errorf("error reading %s: %w", filepath.Join(pmuDir, entPath), err)
		}
		if err := f(ent.Name(), string(b)); err != nil {
			return fmt.Errorf("%w (from %s)", err, filepath.Join(pmuDir, entPath))
		}
	}
	return nil
}

// pmuParseFormat parses the content of a format description from
// /sys/bus/event_source/devices/*/format/*.
func pmuParseFormat(s string) (pmuFormat, error) {
	// See https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-bus-event_source-devices-format
	// Perf assumes the bit ranges will always be in order,
	// but that doesn't seem right.
	s = strings.TrimRight(s, "\n")
	field, ranges, ok := strings.Cut(s, ":")
	if !ok {
		return pmuFormat{}, fmt.Errorf("error parsing format %q", s)
	}
	var format pmuFormat
	switch string(field) {
	case "config":
		format.field = fieldConfig
	case "config1":
		format.field = fieldConfig1
	case "config2":
		format.field = fieldConfig2
	default:
		return pmuFormat{}, fmt.Errorf("error parsing format %q: unknown field %s", s, field)
	}
	for _, r := range strings.Split(ranges, ",") {
		lo, hi, ok := strings.Cut(r, "-")
		shift, err := strconv.Atoi(lo)
		nBits := 1
		if ok {
			hiVal, err2 := strconv.Atoi(hi)
			if err == nil {
				err = err2
			}
			nBits = hiVal - shift + 1
		}
		if err != nil {
			return pmuFormat{}, fmt.Errorf("error parsing format %q: %w", s, err)
		}
		format.bits = append(format.bits, formatBitRange{shift, nBits})
	}
	return format, nil
}
