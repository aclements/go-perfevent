// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package perf

import (
	"testing"

	"github.com/aclements/go-perfevent/events"
)

func TestOpenOne(t *testing.T) {
	c, err := OpenCounter(TargetThisGoroutine, events.EventCPUCycles)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	doRead := func(min Count) Count {
		t.Helper()
		count, err := c.ReadOne()
		if err != nil {
			t.Fatal("read failed:", err)
		}
		t.Logf("read %+v", count)
		checkCount(t, count, min)
		return count
	}

	c1 := doRead(Count{})
	if c1.RawValue != 0 || c1.TimeEnabled != 0 {
		t.Fatal("counter is non-zero before starting")
	}

	t.Log("starting counter")
	c.Start()
	c2 := doRead(c1)

	t.Log("stopping counter")
	c.Stop()
	c3 := doRead(c2)
	c4 := doRead(c2)
	if c3 != c4 {
		t.Fatal("counter changed while stopped")
	}
}

func TestOpenGroup(t *testing.T) {
	c, err := OpenCounter(TargetThisGoroutine, events.EventCPUCycles, events.EventInstructions)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	doRead := func(min [2]Count) [2]Count {
		t.Helper()
		var counts [2]Count
		err := c.ReadGroup(counts[:])
		if err != nil {
			t.Fatal("read failed:", err)
		}
		t.Logf("read %+v", counts)
		for i, count := range counts {
			checkCount(t, count, min[i])
		}
		return counts
	}

	c1s := doRead([2]Count{})
	for _, c1 := range c1s {
		if c1.RawValue != 0 || c1.TimeEnabled != 0 {
			t.Fatal("counter is non-zero before starting")
		}
	}

	t.Log("starting counter")
	c.Start()
	c2s := doRead(c1s)

	t.Log("stopping counter")
	c.Stop()
	c3 := doRead(c2s)
	c4 := doRead(c2s)
	if c3 != c4 {
		t.Fatal("counter changed while stopped")
	}

	c3x, err := c.ReadOne()
	if err != nil {
		t.Fatal(err)
	}
	if c3x != c3[0] {
		t.Fatalf("ReadOne returned %+v, expected %+v", c3x, c3[0])
	}
}

func checkCount(t *testing.T, count Count, min Count) {
	t.Helper()
	if count.TimeRunning > count.TimeEnabled {
		t.Fatal("TimeRunning > TimeEnabled")
	}
	if count.RawValue < min.RawValue {
		t.Fatal("RawValue decreased")
	}
	if count.TimeEnabled < min.TimeEnabled {
		t.Fatal("TimeEnabled decreased")
	}
	if count.TimeRunning < min.TimeRunning {
		t.Fatal("TimeRunning decreased")
	}
}
