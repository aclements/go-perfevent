// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perfbench

import (
	"fmt"
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
}

type countersOS struct {
	b  testingB
	bN int

	events   []events.Event
	counters []*perf.Counter
	baseline []perf.Count
}

var printUnits = sync.OnceFunc(func() {
	// Print unit metadata.
	for _, event := range defaultEvents {
		// Currently all events are better=lower.
		fmt.Printf("Unit %s better=lower\n", event.String())
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
	events := defaultEvents
	cs := &Counters{countersOS{
		b:        b,
		bN:       bN,
		events:   events,
		counters: make([]*perf.Counter, len(events)),
		baseline: make([]perf.Count, len(events)),
	}}

	for i, event := range cs.events {
		var err error
		cs.counters[i], err = perf.OpenCounter(perf.TargetThisGoroutine, event)
		if err != nil {
			// Only report each error once, to avoid flooding benchmark log.
			msg := fmt.Sprintf("error opening counter %s: %v", event, err)
			if _, prev := openErrors.Swap(msg, true); !prev {
				b.Logf("%s", msg)
			}
		}
	}

	b.Cleanup(cs.close)

	// Start all of the counters.
	cs.Start()

	return cs
}

func (cs *Counters) startOS() {
	for _, c := range cs.counters {
		c.Start()
	}
}

func (cs *Counters) stopOS() {
	for _, c := range cs.counters {
		c.Stop()
	}
}

func (cs *Counters) resetOS() {
	// perf has a concept of resetting a counter, but it doesn't reset the
	// counter's timers, so instead we track our own baseline.
	for i, c := range cs.counters {
		cs.baseline[i], _ = c.ReadOne()
	}
}

func (cs *Counters) close() {
	if cs.b == nil {
		return
	}

	cs.Stop()
	for i, c := range cs.counters {
		val, err := c.ReadOne()
		base := cs.baseline[i]
		val.RawValue -= base.RawValue
		val.TimeEnabled -= base.TimeEnabled
		val.TimeRunning -= base.TimeRunning
		if err != nil {
			cs.b.Logf("error reading %s: %v", defaultEvents[i], err)
		} else if val.TimeRunning > 0 {
			cs.b.ReportMetric(float64(val.Value())/float64(cs.bN), defaultEvents[i].String()+"/op")
		}
		c.Close()
	}
	cs.b = nil
}
