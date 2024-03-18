// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perfbench

import (
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/aclements/go-perfevent/events"
	"github.com/aclements/go-perfevent/perf"
)

// TODO: Support derived events that use event groups.

var defaultEvents = []events.Event{
	events.EventCPUCycles,
	events.EventInstructions,
	events.EventCacheMisses,
	events.EventCacheReferences,
	events.EventBranches,
	getEvent("l1d-loads"),
	getEvent("l1d-load-misses"),
}

func getEvent(name string) events.Event {
	ev, err := events.ParseEvent(name)
	if err != nil {
		// The events we look up are currently all built-in, so parsing should
		// never fail. If we change this in the future, this should synthesize
		// an event that fails on SetAttr.
		panic("failed to parse built-in event " + name)
	}
	return ev
}

type countersOS struct {
	b  testingB
	bN int

	c []counter
}

type counter struct {
	event    events.Event
	counter  *perf.Counter
	name     string
	baseline perf.Count
}

var printUnits = sync.OnceFunc(func() {
	// Print unit metadata.
	for _, event := range defaultEvents {
		// Currently all events are better=lower.
		fmt.Printf("Unit %s/op better=lower\n", event.String())
	}
	fmt.Printf("\n")
})

// testingB is the *testing.B interface needed by Counters. Used for testing.
type testingB interface {
	ReportMetric(n float64, unit string)
	Logf(format string, args ...any)
	Cleanup(func())
}

var openErrors sync.Map

func openOS(b *testing.B) *Counters {
	printUnits()
	return open(b, b.N)
}

func open(b testingB, bN int) *Counters {
	cs := &Counters{countersOS{
		b:  b,
		bN: bN,
		c:  make([]counter, len(defaultEvents)),
	}}

	for i, event := range defaultEvents {
		c, err := perf.OpenCounter(perf.TargetThisGoroutine, event)
		if err != nil {
			// Only report each error once, to avoid flooding benchmark log.
			msg := fmt.Sprintf("error opening counter %s: %v", event, err)
			if _, prev := openErrors.Swap(msg, true); !prev {
				b.Logf("%s", msg)
				continue
			}
		}
		name := event.String()
		if ev, ok := event.(events.EventScale); ok {
			_, unit := ev.ScaleUnit()
			if unit != "" {
				name = name + "-" + unit
			}
		}

		cs.c[i] = counter{event, c, name, perf.Count{}}
	}

	b.Cleanup(cs.close)

	// Start all of the counters.
	cs.Start()

	return cs
}

func (cs *Counters) startOS() {
	for _, c := range cs.c {
		c.counter.Start()
	}
}

func (cs *Counters) stopOS() {
	for _, c := range cs.c {
		c.counter.Stop()
	}
}

func (cs *Counters) resetOS() {
	// perf has a concept of resetting a counter, but it doesn't reset the
	// counter's timers, so instead we track our own baseline.
	for i := range cs.c {
		c := &cs.c[i]
		c.baseline, _ = c.counter.ReadOne()
	}
}

func (c *counter) read() (float64, error) {
	val, err := c.counter.ReadOne()
	base := c.baseline
	val.RawValue -= base.RawValue
	val.TimeEnabled -= base.TimeEnabled
	val.TimeRunning -= base.TimeRunning
	if err != nil {
		return 0, fmt.Errorf("error reading %s: %w", c.event, err)
	} else if val.TimeRunning == 0 {
		return math.Inf(1), nil
	}
	x, _ := val.Value()
	return x, nil
}

func (cs *Counters) totalOS(name string) (float64, bool) {
	for i := range cs.c {
		if name == cs.c[i].name {
			val, err := cs.c[i].read()
			if err != nil {
				return 0, false
			}
			return val, true
		}
	}
	return 0, false
}

func (cs *Counters) close() {
	if cs.b == nil {
		return
	}

	cs.Stop()
	for i := range cs.c {
		c := &cs.c[i]
		if val, err := c.read(); err != nil {
			cs.b.Logf("%s", err)
		} else if !math.IsInf(val, 0) {
			cs.b.ReportMetric(val/float64(cs.bN), c.name+"/op")
		}
		c.counter.Close()
	}
	cs.b = nil
}
