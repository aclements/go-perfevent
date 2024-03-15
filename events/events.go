// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package events

import "golang.org/x/sys/unix"

// An Event represents a performance event that perf can count.
type Event interface {
	// String returns the string representation of this event, preferably as the
	// name used by "perf record -e".
	String() string

	// SetAttrs sets the attributes for this event in the [unix.PerfEventAttr]
	// struct.
	SetAttrs(*unix.PerfEventAttr) error
}

// An EventScale is an Event that provides a scaling factor and unit to convert
// raw values into meaningful values.
type EventScale interface {
	Event

	// ScaleUnit returns the factor to multiply raw values by to compute a
	// meaningful value, plus the unit of that value. A no-op implementation
	// should return 1.0, "".
	ScaleUnit() (scale float64, unit string)
}

type eventBasic struct {
	name   string
	typ    uint32
	config uint64
}

func (e eventBasic) SetAttrs(a *unix.PerfEventAttr) error {
	a.Type = e.typ
	a.Config = e.config
	return nil
}

func (e eventBasic) String() string {
	return e.name
}

var (
	EventCPUCycles       = eventBasic{"cpu-cycles", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_CPU_CYCLES}
	EventInstructions    = eventBasic{"instructions", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_INSTRUCTIONS}
	EventCacheReferences = eventBasic{"cache-references", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_CACHE_REFERENCES}
	EventCacheMisses     = eventBasic{"cache-misses", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_CACHE_MISSES}
	EventBranches        = eventBasic{"branches", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_BRANCH_INSTRUCTIONS}
	EventBranchesMisses  = eventBasic{"branch-misses", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_BRANCH_MISSES}
	EventBusCycles       = eventBasic{"bus-cycles", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_BUS_CYCLES}
)
