// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// perfbench is a utility for counting performance events in a Go benchmark.
package perfbench

import (
	"testing"

	"github.com/aclements/go-perfevent/events"
	"github.com/aclements/go-perfevent/perf"
)

// TODO: Support derived events that use event groups.

// TODO: The difference between the benchmark timer starting automatically and
// these counters not is annoying. Start them automatically so in the basic case
// you can just defer Open().Close(), and provide a Reset method.

var defaultEvents = []events.Event{
	events.EventCPUCycles,
	events.EventInstructions,
	events.EventCacheMisses,
	events.EventCacheReferences,
}

type Counters struct {
	b *testing.B

	events   []events.Event
	counters []*perf.Counter
}

func Open(b *testing.B) *Counters {
	cs := Counters{b: b, events: defaultEvents, counters: make([]*perf.Counter, len(defaultEvents))}

	for i, event := range cs.events {
		var err error
		cs.counters[i], err = perf.OpenCounter(perf.TargetThisGoroutine, event)
		if err != nil {
			b.Logf("error opening counter %s: %v", event, err)
		}
	}

	return &cs
}

func (cs *Counters) Start() {
	for _, c := range cs.counters {
		c.Start()
	}
}

func (cs *Counters) Stop() {
	for _, c := range cs.counters {
		c.Stop()
	}
}

func (cs *Counters) Close() {
	if cs.b == nil {
		return
	}

	cs.Stop()
	for i, c := range cs.counters {
		val, err := c.ReadOne()
		if err != nil {
			cs.b.Logf("error reading %s: %v", defaultEvents[i], err)
		} else if val.TimeRunning > 0 {
			cs.b.ReportMetric(float64(val.Value())/float64(cs.b.N), defaultEvents[i].String()+"/op")
		}
		c.Close()
	}
	cs.b = nil
}
