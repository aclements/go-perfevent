// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package events

import (
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type builtinEvent struct {
	pmu    uint32
	config uint64
}

type cacheEventName struct {
	name   string
	config uint64
}

// builtinEvents are the event names that correspond to well-known perf event
// configs and thus generally don't appear in /sys.
var builtinEvents struct {
	cpu      map[string]builtinEvent // No PMU or cpu/ PMU
	software map[string]builtinEvent // No PMU

	cache        []cacheEventName
	cacheOp      []cacheEventName
	cacheResult  []cacheEventName
	cacheAllowed map[uint64]uint8 // Cache level -> bitmap of cache op

	once sync.Once
}

func resolveBuiltinEvent(pmu, eventName string) (builtinEvent, bool) {
	builtinEvents.once.Do(func() {
		// See parse-events.c:event_symbols_hw
		builtinEvents.cpu = make(map[string]builtinEvent)
		hw := func(config uint64, names ...string) {
			ev := builtinEvent{unix.PERF_TYPE_HARDWARE, config}
			for _, name := range names {
				builtinEvents.cpu[name] = ev
			}
		}
		hw(unix.PERF_COUNT_HW_CPU_CYCLES, "cpu-cycles", "cycles")
		hw(unix.PERF_COUNT_HW_INSTRUCTIONS, "instructions")
		hw(unix.PERF_COUNT_HW_CACHE_REFERENCES, "cache-references")
		hw(unix.PERF_COUNT_HW_CACHE_MISSES, "cache-misses")
		hw(unix.PERF_COUNT_HW_BRANCH_INSTRUCTIONS, "branch-instructions", "branches")
		hw(unix.PERF_COUNT_HW_BRANCH_MISSES, "branch-misses")
		hw(unix.PERF_COUNT_HW_BUS_CYCLES, "bus-cycles")
		hw(unix.PERF_COUNT_HW_STALLED_CYCLES_FRONTEND, "stalled-cycles-frontend", "idle-cycles-frontend")
		hw(unix.PERF_COUNT_HW_STALLED_CYCLES_BACKEND, "stalled-cycles-backend", "idle-cycles-backend")
		hw(unix.PERF_COUNT_HW_REF_CPU_CYCLES, "ref-cycles")

		// See parse-events.c:event_symbols_sw
		builtinEvents.software = make(map[string]builtinEvent)
		sw := func(config uint64, names ...string) {
			ev := builtinEvent{unix.PERF_TYPE_SOFTWARE, config}
			for _, name := range names {
				builtinEvents.software[name] = ev
			}
		}
		sw(unix.PERF_COUNT_SW_CPU_CLOCK, "cpu-clock")
		sw(unix.PERF_COUNT_SW_TASK_CLOCK, "task-clock")
		sw(unix.PERF_COUNT_SW_PAGE_FAULTS, "page-faults", "faults")
		sw(unix.PERF_COUNT_SW_CONTEXT_SWITCHES, "context-switches", "cs")
		sw(unix.PERF_COUNT_SW_CPU_MIGRATIONS, "cpu-migrations", "migrations")
		sw(unix.PERF_COUNT_SW_PAGE_FAULTS_MIN, "minor-faults")
		sw(unix.PERF_COUNT_SW_PAGE_FAULTS_MAJ, "major-faults")
		sw(unix.PERF_COUNT_SW_ALIGNMENT_FAULTS, "alignment-faults")
		sw(unix.PERF_COUNT_SW_EMULATION_FAULTS, "emulation-faults")
		sw(unix.PERF_COUNT_SW_DUMMY, "dummy")
		sw(unix.PERF_COUNT_SW_BPF_OUTPUT, "bpf-output")
		// The unix package doesn't know this one.
		//sw(unix.PERF_COUNT_SW_CGROUP_SWITCHES, "cgroup-switches")

		var m *[]cacheEventName
		c := func(config uint64, names ...string) {
			for _, name := range names {
				(*m) = append(*m, cacheEventName{name, config})
			}
		}
		cSort := func() {
			// Put longer names earlier for matching
			sort.Slice(*m, func(i, j int) bool {
				return len((*m)[i].name) > len((*m)[j].name)
			})
		}
		// See evsel.c:evsel__hw_cache
		m = &builtinEvents.cache
		c(unix.PERF_COUNT_HW_CACHE_L1D, "L1-dcache", "l1-d", "l1d", "L1-data")
		c(unix.PERF_COUNT_HW_CACHE_L1I, "L1-icache", "l1-i", "l1i", "L1-instruction")
		c(unix.PERF_COUNT_HW_CACHE_LL, "LLC", "L2")
		c(unix.PERF_COUNT_HW_CACHE_DTLB, "dTLB", "d-tlb", "Data-TLB")
		c(unix.PERF_COUNT_HW_CACHE_ITLB, "iTLB", "i-tlb", "Instruction-TLB")
		c(unix.PERF_COUNT_HW_CACHE_BPU, "branch", "branches", "bpu", "btb", "bpc")
		c(unix.PERF_COUNT_HW_CACHE_NODE, "node")
		cSort()
		// See evsel.c:evsel__hw_cache_op
		m = &builtinEvents.cacheOp
		c(unix.PERF_COUNT_HW_CACHE_OP_READ, "load", "loads", "read")
		c(unix.PERF_COUNT_HW_CACHE_OP_WRITE, "store", "stores", "write")
		c(unix.PERF_COUNT_HW_CACHE_OP_PREFETCH, "prefetch", "prefetches", "speculative-read", "speculative-load")
		cSort()
		// evsel.c:evsel__hw_cache_result
		m = &builtinEvents.cacheResult
		c(unix.PERF_COUNT_HW_CACHE_RESULT_ACCESS, "refs", "Reference", "ops", "access")
		c(unix.PERF_COUNT_HW_CACHE_RESULT_MISS, "misses", "miss")
		cSort()

		r := uint8(1) << unix.PERF_COUNT_HW_CACHE_OP_READ
		w := uint8(1) << unix.PERF_COUNT_HW_CACHE_OP_WRITE
		p := uint8(1) << unix.PERF_COUNT_HW_CACHE_OP_PREFETCH
		builtinEvents.cacheAllowed = map[uint64]uint8{
			unix.PERF_COUNT_HW_CACHE_L1D:  r | w | p,
			unix.PERF_COUNT_HW_CACHE_L1I:  r | p,
			unix.PERF_COUNT_HW_CACHE_LL:   r | w | p,
			unix.PERF_COUNT_HW_CACHE_DTLB: r | w | p,
			unix.PERF_COUNT_HW_CACHE_ITLB: r,
			unix.PERF_COUNT_HW_CACHE_BPU:  r,
			unix.PERF_COUNT_HW_CACHE_NODE: r | w | p,
		}
	})

	// All builtin events are either under no PMU or under cpu/.
	if !(pmu == "" || pmu == "cpu") {
		return builtinEvent{}, false
	}

	// CPU events can be used with or without a PMU name.
	if e, ok := builtinEvents.cpu[eventName]; ok {
		return e, true
	}

	// Software events can only be used with no PMU name.
	if pmu == "" {
		if e, ok := builtinEvents.software[eventName]; ok {
			return e, true
		}
	}

	// Try to parse it as a cache event name, which can be used with or without
	// a PMU name. See parse-events.c:parse_events__decode_legacy_cache and
	// parse-events.l:PE_LEGACY_CACHE.
	findCache := func(s string, names []cacheEventName) (uint64, string, bool) {
		for i := range names {
			name := names[i].name
			if s == name {
				return names[i].config, "", true
			}
			if strings.HasPrefix(s, name) && s[len(name)] == '-' {
				return names[i].config, s[len(name)+1:], true
			}
		}
		return 0, "", false
	}
	if config, s, ok := findCache(eventName, builtinEvents.cache); ok {
		// Perf accepts up to two more fields that are op and result. It will
		// even accept nonsense like l1d-loads-stores, but I don't think that's
		// intentional because the lexer doesn't distinguish between op and
		// result, but the parser then fails to parse the third field and just
		// ignores the failure.
		op := uint64(unix.PERF_COUNT_HW_CACHE_OP_READ)
		result := uint64(unix.PERF_COUNT_HW_CACHE_RESULT_ACCESS)
		var haveOp, haveResult bool
		for i := 0; i < 2 && s != ""; i++ {
			if !haveOp {
				if op2, s2, ok := findCache(s, builtinEvents.cacheOp); ok {
					op, s, haveOp = op2, s2, true
					continue
				}
			}
			if !haveResult {
				if result2, s2, ok := findCache(s, builtinEvents.cacheResult); ok {
					result, s, haveResult = result2, s2, true
					continue
				}
			}
		}
		if s == "" {
			// Parsed the whole event. Check if it's an allowed combination.
			if builtinEvents.cacheAllowed[config]&(1<<op) != 0 {
				config |= (op << 8) | (result << 16)
				return builtinEvent{unix.PERF_TYPE_HW_CACHE, config}, true
			}
		}
	}

	return builtinEvent{}, false
}
