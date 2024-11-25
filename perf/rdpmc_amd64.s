// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "textflag.h"

// func rdpmc(counter uint32) int64
TEXT ·rdpmc(SB), NOSPLIT, $8-8
	// RDPMC is not a serializing instruction and thus may run out of order
	// w.r.t. the events we want to count. Add explicit serialization.
	MOVL	$0, AX
	CPUID

	MOVL	counter+0(FP), CX
	RDPMC
	MOVL	AX, ret_lo+8(FP)
	MOVL	DX, ret_hi+12(FP)

	RET
