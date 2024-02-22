// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// perfbench is a utility for counting performance events in a Go benchmark.
package perfbench

import (
	"testing"

	events1 "github.com/aclements/go-perfevent/events"
	"github.com/aclements/go-perfevent/perf"
)

// TODO: Support derived events that use event groups.

var events = []events1.Event{
	events1.EventCPUCycles,
	events1.EventInstructions,
	events1.EventCacheMisses,
	events1.EventCacheReferences,
}

type Counters struct {
	b *testing.B

	events   []events1.Event
	counters []*perf.Counter
}

func Open(b *testing.B) *Counters {
	cs := Counters{b: b, events: events, counters: make([]*perf.Counter, len(events))}

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
			cs.b.Logf("error reading %s: %v", events[i], err)
		} else if val.TimeRunning > 0 {
			cs.b.ReportMetric(float64(val.Value())/float64(cs.b.N), events[i].String()+"/op")
		}
		c.Close()
	}
	cs.b = nil
}
