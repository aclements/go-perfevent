// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !linux

package perfbench

import "testing"

type countersOS struct{}

func openOS(*testing.B) *Counters {
	return nil
}

func (cs *Counters) startOS() {}

func (cs *Counters) stopOS() {}

func (cs *Counters) resetOS() {}

func (cs *Counters) totalOS(_ string) (float64, bool) { return 0, false }
