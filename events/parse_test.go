// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package events

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

//go:embed testdata/pmufs
var testPMUFS embed.FS

//go:embed testdata/perf-list-j
var testPerfListJ []byte

func init() {
	// Switch to a baked-in fake PMU file system so we don't depend on the system.
	pmuDir = "testdata/pmufs"
	pmuFS, _ = fs.Sub(testPMUFS, pmuDir)

	// Stub the perf command with real data (albeit minimized).
	perfListHook = func(outBuf io.Writer) {
		outBuf.Write(testPerfListJ)
	}
}

func evString(ev Event) string {
	if ev == nil {
		return "<nil>"
	}

	var attrs unix.PerfEventAttr
	if err := ev.SetAttrs(&attrs); err != nil {
		return fmt.Sprintf("(error %s)", err)
	}

	if attrs.Type == ^uint32(0) {
		// We use this in tests to indicate an invalid event.
		return "<invalid>"
	}

	var s strings.Builder
	fmt.Fprintf(&s, "pmu%d/config=%#x", attrs.Type, attrs.Config)
	if attrs.Ext1 != 0 {
		fmt.Fprintf(&s, ",config1=%#x", attrs.Ext1)
	}
	if attrs.Ext2 != 0 {
		fmt.Fprintf(&s, ",config2=%#x", attrs.Ext2)
	}
	if attrs.Sample != 0 {
		fmt.Fprintf(&s, ",period=%#x", attrs.Sample)
	}
	s.WriteByte('/')
	return s.String()
}

func TestParseBuiltin(t *testing.T) {
	for _, tc := range getBuiltinTests() {
		// Test via parseBuiltinEvent
		gotBE, ok := resolveBuiltinEvent(tc.pmuName, tc.eventName)
		if !ok {
			gotBE = builtinEvent{^uint32(0), 0}
		}
		wantBE := builtinEvent{tc.pmu, tc.config}
		if wantBE != gotBE {
			t.Errorf("PMU %q, event %q: got %s, want %s", tc.pmuName, tc.eventName, evString(gotBE), evString(wantBE))
			// If this is messed up, skip ParseEvent.
			continue
		}

		// Test via ParseEvent.
		var eventName string
		if tc.pmuName != "" {
			eventName = tc.pmuName + "/" + tc.eventName + "/"
		} else {
			eventName = tc.eventName
		}
		gotEv, err := ParseEvent(eventName)
		if err != nil {
			gotBE = builtinEvent{pmu: ^uint32(0)}
		} else {
			gotBE = gotEv.(builtinEvent)
		}
		if wantBE != gotBE {
			t.Errorf("%s: got %s (err %s), want %s", eventName, evString(gotBE), err, evString(wantBE))
		}
	}
}

type builtinTest struct {
	pmuName   string
	eventName string

	pmu    uint32
	config uint64
}

func getBuiltinTests() []builtinTest {
	var tests []builtinTest

	bad := func(pmu, config string) {
		tests = append(tests,
			builtinTest{pmu, config, ^uint32(0), 0})
	}

	hw := func(config uint64, name string) {
		tests = append(tests,
			builtinTest{"cpu", name, unix.PERF_TYPE_HARDWARE, config},
			builtinTest{"", name, unix.PERF_TYPE_HARDWARE, config},
		)
		bad("xxx", name)
	}
	hw(unix.PERF_COUNT_HW_CPU_CYCLES, "cpu-cycles")
	hw(unix.PERF_COUNT_HW_CPU_CYCLES, "cycles")
	// "branches" could be interpreted as either
	// PERF_COUNT_HW_BRANCH_INSTRUCTIONS or PERF_COUNT_HW_CACHE_BPU, but perf
	// prefers to interpret as the former.
	hw(unix.PERF_COUNT_HW_BRANCH_INSTRUCTIONS, "branches")
	hw(unix.PERF_COUNT_HW_REF_CPU_CYCLES, "ref-cycles")

	sw := func(config uint64, name string) {
		tests = append(tests,
			builtinTest{"", name, unix.PERF_TYPE_SOFTWARE, config},
		)
		bad("cpu", name)
		bad("xxx", name)
	}
	sw(unix.PERF_COUNT_SW_CPU_CLOCK, "cpu-clock")
	sw(unix.PERF_COUNT_SW_CONTEXT_SWITCHES, "context-switches")
	sw(unix.PERF_COUNT_SW_CONTEXT_SWITCHES, "cs")

	cache := func(level, op, result uint64, names ...string) {
		config := level | (op << 8) | (result << 16)
		for _, name := range names {
			tests = append(tests,
				builtinTest{"cpu", name, unix.PERF_TYPE_HW_CACHE, config},
				builtinTest{"", name, unix.PERF_TYPE_HW_CACHE, config},
			)
			bad("xxx", name)
			bad("", name+"x")
			bad("", name+"-x")
			bad("", "x-"+name)
		}
	}
	cache(unix.PERF_COUNT_HW_CACHE_L1D, unix.PERF_COUNT_HW_CACHE_OP_READ, unix.PERF_COUNT_HW_CACHE_RESULT_ACCESS,
		"L1-dcache", "l1d", "L1-dcache-read", "l1d-loads", "l1d-load-refs", "l1d-refs", "l1d-read-access")
	// Perf accepts this, but it's nonsense. The perf yacc grammar doesn't
	// distinguish between op and result, then the C parser gets confused and
	// stops at the second "-", but without an error.
	bad("", "l1d-loads-stores")
	cache(unix.PERF_COUNT_HW_CACHE_L1D, unix.PERF_COUNT_HW_CACHE_OP_PREFETCH, unix.PERF_COUNT_HW_CACHE_RESULT_MISS,
		"L1-dcache-prefetch-miss", "L1-dcache-speculative-load-misses")
	cache(unix.PERF_COUNT_HW_CACHE_BPU, unix.PERF_COUNT_HW_CACHE_OP_READ, unix.PERF_COUNT_HW_CACHE_RESULT_ACCESS,
		"branch", "branches-loads", "bpu-read", "bpu-loads-refs", "bpu-Reference")
	bad("", "bpu-stores") // Disallowed combination

	return tests
}

func (ev *rawEvent) c1(val uint64) *rawEvent {
	ev.config1 = val
	return ev
}
func (ev *rawEvent) c2(val uint64) *rawEvent {
	ev.config2 = val
	return ev
}
func (ev *rawEvent) p(val uint64) *rawEvent {
	ev.period = val
	return ev
}
func (ev *rawEvent) setScale(scale float64, unit string) *rawEvent {
	ev.scale = scale
	ev.unit = unit
	return ev
}

func TestParse(t *testing.T) {
	test := func(name string, want *rawEvent) {
		t.Helper()
		got, err := ParseEvent(name)
		if err != nil {
			t.Errorf("%s: want %s, got error %s", name, evString(want), err)
			return
		}
		var wantAttrs, gotAttrs unix.PerfEventAttr
		want.SetAttrs(&wantAttrs)
		got.SetAttrs(&gotAttrs)
		if wantAttrs != gotAttrs {
			t.Errorf("%s: want %s, got %s", name, evString(want), evString(got))
		}
	}
	testErr := func(name string, want string) {
		t.Helper()
		got, err := ParseEvent(name)
		if err == nil {
			t.Errorf("%s: want error %s, got event %s", name, want, evString(got))
			return
		}
		if err.Error() != want {
			t.Errorf("%s: want error %s, got error %s", name, want, err)
		}
	}
	hw := func(config uint64) *rawEvent {
		return &rawEvent{pmu: unix.PERF_TYPE_HARDWARE, config: config}
	}
	raw := func(config uint64) *rawEvent {
		return &rawEvent{pmu: unix.PERF_TYPE_RAW, config: config}
	}

	// Perf prefers the built-in event even if there's one in /sys
	test("cpu/cpu-cycles/", hw(unix.PERF_COUNT_HW_CPU_CYCLES))
	test("cpu-cycles", hw(unix.PERF_COUNT_HW_CPU_CYCLES))

	// Test an event from /sys
	test("cpu/mem-stores/", raw(0xd0|0x82<<8))
	// Any CPU event can omit the PMU, even if it's not built-in
	test("mem-stores", raw(0xd0|0x82<<8))

	// Test pure parameter events.
	test("cpu/event=0xd0/", raw(0xd0))
	test("cpu/event=42/", raw(42))
	test("cpu/event=042/", raw(0o42))
	test("cpu/event=0xd0,config1=0xd1,config2=0xd2/", raw(0xd0).c1(0xd1).c2(0xd2))
	test("cpu/config=0xd0,config1=0xd1,config2=0xd2/", raw(0xd0).c1(0xd1).c2(0xd2))

	// Test mixing parameters and names.
	test("cpu/mem-stores,umask=42/", raw(0xd0|42<<8))
	test("cpu/umask=42,mem-stores/", raw(0xd0|42<<8))

	// Test a single bit field.
	test("cpu/edge=1/", raw(1<<18))
	test("cpu/edge/", raw(1<<18))

	// Test mixing single bit fields with event names.
	test("cpu/mem-stores,edge/", raw(0xd0|0x82<<8|1<<18))
	test("cpu/edge,mem-stores/", raw(0xd0|0x82<<8|1<<18))

	// Test mixing an event that's both built-in and in /sys with a /sys
	// parameter. Perf will generate a nonsense event for this with type
	// HARDWARE that mixes the fixed config enum with bits from /sys. We'll
	// instead find the event in /sys and use that.
	test("cpu/cpu-cycles,edge/", raw(0x3c|1<<18))

	// Test a format with multiple bit fields.
	test("fake/splitevent=0x1/", raw(1<<0))
	test("fake/splitevent=0x2/", raw(1<<2))
	test("fake/splitevent=0x4/", raw(1<<3))
	test("fake/splitevent=0x8/", raw(1<<5))

	// Test perf list -j events.
	test("l1d.replacement", raw(0x51|0x1<<8).p(0x186a3)) // cpu/event=0x51,period=0x186a3,umask=0x1/
	test("cpu/l1d.replacement/", raw(0x51|0x1<<8).p(0x186a3))

	// Test scaled events from /sys.
	test("fake/scaled/", raw(0).setScale(2.5e-10, "Joules"))
	test("fake/united/", raw(0).setScale(1, "Joules"))
	// Test scaled events from perf list -j.
	test("fakescaled", raw(0).setScale(100, "%"))

	// Test unknown event
	testErr("bad", `unknown event "bad"`)
	testErr("cpu/bad/", `event "cpu/bad/": unknown event or parameter "bad"`)
	// Test unknown PMU
	testErr("bad/cpu-cycles/", `unknown PMU "bad"`)
	// Test parameter out of range
	testErr("cpu/event=0x1ff/", `event "cpu/event=0x1ff/": parameter event=511 not in range 0-255`)
	testErr("cpu/edge=2/", `event "cpu/edge=2/": parameter edge=2 not in range 0-1`)
	testErr("fake/splitevent=0x10/", `event "fake/splitevent=0x10/": parameter splitevent=16 not in range 0-15`)
	// Test unknown parameter
	testErr("cpu/bad=25/", `event "cpu/bad=25/": unknown event or parameter "bad"`)
	// Test multiple events
	testErr("cpu/cpu-cycles,mem-stores/", `event "cpu/cpu-cycles,mem-stores/": multiple events "cpu-cycles" and "mem-stores"`)
	// Test mixing built-in events (that aren't in /sys) with parameters from
	// /sys. Perf will accept these, but then use a built-in type with nonsense
	// bits set from the dynamic PMU configuration. We reject them.
	//
	// This error could be better.
	testErr("cpu/l1d,edge/", `event "cpu/l1d,edge/": unknown event or parameter "l1d"`)
	testErr("cpu/edge,l1d/", `event "cpu/edge,l1d/": unknown event or parameter "l1d"`)
	// Test malformed parameter lists
	testErr("cpu/event=abc/", `event "cpu/event=abc/": error parsing event param list "event=abc": parameter "event=abc" not a number`)
	testErr("cpu/one,two/", `event "cpu/one,two/": unknown event or parameter "one"`)
	testErr("cpu/=1/", `event "cpu/=1/": error parsing event param list "=1": missing parameter name in "=1"`)
}

func TestParsePerfList(t *testing.T) {
	// Test that we can parse everything an example perf list -j.
	testParsePerfList(t, testPerfListJ, nil, nil)
}

func TestParsePerfListHost(t *testing.T) {
	// Test the output of perf list -j from the host perf command.
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd := exec.Command("perf", "list", "-j")
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	testParsePerfList(t, outBuf.Bytes(), errBuf.Bytes(), err)
}

// Test parsing all of the events in perf list -j.
func testParsePerfList(t *testing.T, data, errOut []byte, err error) {
	m, err := parsePerfList(data, errOut, err)
	if err != nil {
		if strings.Contains(err.Error(), "cannot enumerate extended events") {
			t.Skip(err)
		}
		t.Fatalf("failed to parse perf list -j JSON: %s", err)
	}
	cpu, err := pmus.get("cpu")
	if err != nil {
		t.Fatalf("failed to get cpu PMU: %s", err)
	}
	for _, pj := range m {
		if pj.Encoding == "" {
			// Most of these events are actually built-in, and for those that
			// aren't we'll bail before calling toPMUEvent.
			continue
		}
		if pj.Unit != "cpu" {
			// We only look for perf list events under the CPU PMU.
			continue
		}
		var raw rawEvent
		err := pj.toRawEvent(cpu, &raw)
		if err != nil {
			t.Errorf("failed to parse perf list -j event %#v:\n%s", pj, err)
		}
	}
}
