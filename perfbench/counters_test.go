// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package perfbench

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

type testB struct {
	t       *testing.T
	metrics map[string]float64
	cleanup func()
}

func (tb *testB) ReportMetric(n float64, unit string) {
	if tb.metrics == nil {
		tb.metrics = map[string]float64{}
	}
	tb.metrics[unit] = n
}

func (tb *testB) Logf(format string, args ...any) {
	tb.t.Helper()
	tb.t.Fatalf("unexpected b.Logf: %s", fmt.Sprintf(format, args...))
}

func (tb *testB) Cleanup(fn func()) {
	tb.cleanup = fn
}

func TestBasic(t *testing.T) {
	tb := &testB{t: t}
	open(tb, 1)
	tb.cleanup()

	// Check that metrics were reported.
	for _, ev := range defaultEvents {
		name := ev.String() + "/op"
		if val, ok := tb.metrics[name]; !ok {
			t.Errorf("metric %s not reported", name)
		} else if val == 0 {
			if strings.HasPrefix(name, "cache-") {
				// It's pretty easy for cache counters to be 0 in this little
				// test.
				continue
			}
			t.Errorf("metric %s reported, but value is 0", name)
		}
	}
	if len(tb.metrics) != len(defaultEvents) {
		t.Errorf("got %d metrics, expected %d", len(tb.metrics), len(defaultEvents))
	}
}

var loopIters = 1000

// measureLoop returns the instructions/op of a range loop to 1000. This is used
// for several tests below.
func measureLoop(t *testing.T) float64 {
	p95 := p95Of(100, func() float64 {
		tb := &testB{t: t}
		open(tb, 1)
		for i := 0; i < loopIters; i++ {
		}
		tb.cleanup()
		// The instructions counter should be pretty stable.
		return tb.metrics["instructions/op"]
	})
	t.Logf("loop is %f instructions (p95)", p95)
	if p95 < 1000 {
		t.Fatalf("failed to count loop instructions")
	}
	return p95
}

func p95Of(iters int, f func() float64) float64 {
	dist := make([]float64, iters)
	for i := range dist {
		dist[i] = f()
	}
	slices.Sort(dist)
	return dist[int(float64(iters)*95/100+0.5)]
}

const slack = 1.5

func TestStop(t *testing.T) {
	limit := measureLoop(t) * slack

	// Occasionally we get unlucky (e.g., kernel preemption). Do a bunch of
	// tests and ignore the outliers.
	p95 := p95Of(100, func() float64 {
		tb := &testB{t: t}
		cs := open(tb, 1)
		for i := 0; i < loopIters; i++ {
		}
		cs.Stop()
		for i := 0; i < 100*loopIters; i++ {
		}
		tb.cleanup()
		return tb.metrics["instructions/op"]
	})
	if p95 > limit {
		t.Errorf("stop didn't stop counter, got %f > %f instructions", p95, limit)
	}
}

func TestResetStopped(t *testing.T) {
	tb := &testB{t: t}
	cs := open(tb, 1)
	cs.Stop()
	cs.Reset()
	for i := 0; i < loopIters; i++ {
	}
	tb.cleanup()

	if tb.metrics["instructions/op"] != 0 {
		t.Errorf("reset didn't reset instructions to 0")
	}
}

func TestResetRunning(t *testing.T) {
	limit := measureLoop(t) * slack

	p95 := p95Of(100, func() float64 {
		tb := &testB{t: t}
		cs := open(tb, 1)
		for i := 0; i < 100*loopIters; i++ {
		}
		cs.Reset()
		for i := 0; i < loopIters; i++ {
		}
		cs.Stop()
		tb.cleanup()
		return tb.metrics["instructions/op"]
	})

	if p95 > limit {
		t.Errorf("reset didn't reset counter, got %f > %f instructions", p95, limit)
	}
}
