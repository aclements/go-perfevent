// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// perfbench is a utility for counting performance events in a Go benchmark.
package perfbench

import "testing"

// TODO: Sometimes you want to use custom counters in benchmarks and get the
// nice integration with testing.B, but not just automatically report them as
// X/op. Something between the perf package and the current perfbench package.

// Counters is a set of performance counters that will be reported in benchmark
// results.
type Counters struct {
	countersOS
}

// Open starts a set of performance counters for benchmark b. These counters
// will be reported as metrics when the benchmark ends. The counters only count
// performance events on the calling goroutine.
//
// The counters are running on return. In general, any calls to b.StopTimer,
// b.StartTimer, or b.ResetTimer should be paired with the equivalent calls on
// Counters.
//
// The final value of the counters is captured in a b.Cleanup function. If the
// benchmark does substantial other work in cleanup functions, it may want to
// explicitly call [Counters.Stop] before returning.
func Open(b *testing.B) *Counters {
	return openOS(b)
}

func (cs *Counters) Start() {
	cs.startOS()
}

func (cs *Counters) Stop() {
	cs.stopOS()
}

func (cs *Counters) Reset() {
	cs.resetOS()
}

// Total returns the total count of the named counter, which is a reported
// metric name without the "/op". If the named counter is unknown or could not
// be opened, this returns 0, false.
func (cs *Counters) Total(name string) (float64, bool) {
	return cs.totalOS(name)
}
