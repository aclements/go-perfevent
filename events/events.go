// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package events

// An Event represents a performance event that perf can count.
type Event interface {
	eventOS

	// String returns the string representation of this event, preferably as the
	// name used by "perf record -e".
	String() string

	isEvent()
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
