// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package perf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/aclements/go-perfevent/events"
)

// Target specifies what goroutine, thread, or CPU a [Counter] should monitor.
type Target interface {
	pidCPU() (pid, cpu int)
	open()
	close()
}

type targetThisGoroutine struct{}

func (targetThisGoroutine) pidCPU() (pid, cpu int) { return 0, -1 }
func (targetThisGoroutine) open()                  { runtime.LockOSThread() }
func (targetThisGoroutine) close()                 { runtime.UnlockOSThread() }

var (
	// TargetThisGoroutine monitors the calling goroutine. This will call
	// [runtime.LockOSThread] on Open and [runtime.UnlockOSThread] on Close.
	TargetThisGoroutine = targetThisGoroutine{}
)

// A Counter reports the number of times a [events.Event] or group of Events
// occurred.
type Counter struct {
	target Target

	eventScales []scale

	f []*os.File

	mmap []*unix.PerfEventMmapPage

	running bool

	nEvents int
	readBuf []byte
}

type scale struct {
	scale float64
	unit  string
}

const (
	// Version of PerfEventMmapPage we understand.
	perfEventMmapPageVersion = 0

	capabilityRDPMC = 1 << 2
)

// OpenCounter returns a new [Counter] that reads values for the given
// [events.Event] or group of Events on the given [Target]. Callers are
// expected to call [Counter.Close] when done with this Counter.
//
// If multiple events are given, they are opened as a group, which means they
// will all be scheduled onto the hardware at the same time.
//
// The counter is initially not running. Call [Counter.Start] to start it.
func OpenCounter(target Target, evs ...events.Event) (*Counter, error) {
	if len(evs) == 0 {
		return nil, nil
	}

	// Get event scales.
	eventScales := make([]scale, len(evs))
	for i, event := range evs {
		sc, unit := 1.0, ""
		if es, ok := event.(events.EventScale); ok {
			sc, unit = es.ScaleUnit()
		}
		eventScales[i] = scale{sc, unit}
	}

	pid, cpu := target.pidCPU()

	// Open the group leader.
	attr := unix.PerfEventAttr{}
	attr.Size = uint32(unsafe.Sizeof(attr))
	if err := evs[0].SetAttrs(&attr); err != nil {
		return nil, err
	}
	attr.Read_format = unix.PERF_FORMAT_TOTAL_TIME_ENABLED |
		unix.PERF_FORMAT_TOTAL_TIME_RUNNING |
		unix.PERF_FORMAT_GROUP
	attr.Bits = unix.PerfBitDisabled

	// TODO: Allow setting flags that make sense.

	var c Counter
	c.target = target
	c.eventScales = eventScales
	c.nEvents = len(evs)

	success := false
	target.open()
	defer func() {
		if !success {
			target.close()
		}
	}()

	fd, err := unix.PerfEventOpen(&attr, pid, cpu, -1, unix.PERF_FLAG_FD_CLOEXEC)
	if err != nil {
		if errors.Is(err, syscall.EACCES) {
			const path = "/proc/sys/kernel/perf_event_paranoid"
			data, err2 := os.ReadFile(path)
			data = bytes.TrimSpace(data)
			if val, err3 := strconv.Atoi(string(data)); err2 != nil || err3 != nil || val > 0 {
				// We can't read it, or it's set to > 0.
				err = fmt.Errorf("%w (consider: echo 0 | sudo tee %s)", err, path)
			}
		}
		return nil, err
	}
	c.f = append(c.f, os.NewFile(uintptr(fd), "<perf-event>"))

	// We just need the initial metadata page.
	ptr, err := unix.MmapPtr(fd, 0, nil, uintptr(unix.Getpagesize()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("error mapping perf event page: %v", err)
	}
	c.mmap = append(c.mmap, (*unix.PerfEventMmapPage)(ptr))

	defer func() {
		if !success {
			for _, f := range c.f {
				f.Close()
			}
		}
	}()

	// Open other events.
	for _, event := range evs[1:] {
		attr = unix.PerfEventAttr{}
		attr.Size = uint32(unsafe.Sizeof(attr))
		if err := event.SetAttrs(&attr); err != nil {
			return nil, err
		}
		// Note that we do *not* set PerfBitDisabled, since child events run
		// only when both the parent and the child are enabled, and we want all
		// control to be on the parent.

		fd2, err := unix.PerfEventOpen(&attr, pid, cpu, fd, unix.PERF_FLAG_FD_CLOEXEC)
		if err != nil {
			return nil, err
		}

		c.f = append(c.f, os.NewFile(uintptr(fd2), "<perf-event>"))

		ptr, err := unix.MmapPtr(fd2, 0, nil, uintptr(unix.Getpagesize()), unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			return nil, fmt.Errorf("error mapping perf event page: %v", err)
		}
		c.mmap = append(c.mmap, (*unix.PerfEventMmapPage)(ptr))
	}

	// Allocate a large enough read buffer.
	c.readBuf = make([]byte, 3*8+len(evs)*8)

	success = true
	return &c, nil
}

// Close closes this counter and unlocks the goroutine from the OS thread.
func (c *Counter) Close() {
	if c == nil || c.f == nil {
		return
	}
	for _, f := range c.f {
		f.Close()
	}
	c.f = nil
	c.target.close()
	c.target = nil
}

// Start the counter.
func (c *Counter) Start() {
	if c == nil || c.running {
		return
	}
	c.running = true
	unix.IoctlGetInt(int(c.f[0].Fd()), unix.PERF_EVENT_IOC_ENABLE)
}

// Stop the counter.
func (c *Counter) Stop() {
	if c == nil || !c.running {
		return
	}
	unix.IoctlGetInt(int(c.f[0].Fd()), unix.PERF_EVENT_IOC_DISABLE)
	c.running = false
}

// Count is the value of a Counter.
type Count struct {
	RawValue uint64 // The number of events while this counter was running.

	// Normally, TimeEnabled == TimeRunning. However, if more counters are
	// running than the hardware can support, events will be multiplexed onto
	// the hardware. In that case, TimeRunning < TimeEnabled, and the raw
	// counter value should be scaled under the assumption that the event is
	// happening at a regular rate and the sampled time is representative.

	TimeEnabled uint64 // Total time the Counter was started.
	TimeRunning uint64 // Total time the Counter was actually counting.

	scale scale
}

// Value returns the measured value of Count, scaled to account for time the
// counter was scheduled, and to account for any conversion factors in the
// underlying event.
func (c Count) Value() (float64, string) {
	raw := float64(c.RawValue)
	if c.TimeEnabled == c.TimeRunning && c.scale.scale == 1.0 {
		// Common case: it was running the whole time and there's no conversion factor.
		return raw, c.scale.unit
	}
	if c.TimeRunning == 0 {
		// Avoid divide by zero.
		return 0, c.scale.unit
	}
	return raw * (float64(c.TimeEnabled) / float64(c.TimeRunning)) * c.scale.scale, c.scale.unit
}

// ReadOne returns the current value of the first event in c. For counters that
// only have a single Event, this is faster and more ergonomic than
// [Counter.ReadGroup].
func (c *Counter) ReadOne() (Count, error) {
	if c == nil {
		return Count{}, nil
	}

	// Use RDPMC when possible.
	cnt, ok := readPMC(c.mmap[0])
	if ok {
		return cnt, nil
	}

	var cs [1]Count
	if err := c.ReadGroup(cs[:]); err != nil {
		return Count{}, err
	}
	return cs[0], nil
}

// Returns false if reading from PMC is not currently available. Note that this
// may change from one call to the next due to e.g., multiplexing.
func readPMC(mmap *unix.PerfEventMmapPage) (c Count, ok bool) {
	if runtime.GOARCH != "amd64" {
		return c, false
	}

	if mmap.Compat_version > perfEventMmapPageVersion {
		return c, false
	}

	// See Linux tools/lib/perf/mmap.c:perf_mmap__read_self for reference.
	for {
		// TODO(prattmic): This doesn't need to be atomic, but it does
		// need a compiler barrier.
		seq := atomic.LoadUint32(&mmap.Lock)

		if mmap.Capabilities&capabilityRDPMC == 0 {
			return c, false
		}

		idx := mmap.Index
		if idx == 0 {
			return c, false
		}

		c.TimeEnabled = mmap.Time_enabled
		c.TimeRunning = mmap.Time_running

		pmc := rdpmc(idx - 1)
		// Sign extend.
		// TODO(prattmic): perf_event_open(2) mentions this, but what
		// events can be negative?
		pmc <<= 64 - mmap.Pmc_width
		pmc >>= 64 - mmap.Pmc_width

		c.RawValue = uint64(pmc) + uint64(mmap.Offset)

		if atomic.LoadUint32(&mmap.Lock) == seq {
			break
		}
	}

	return c, true
}

// ReadGroup returns the current value of all events in c.
func (c *Counter) ReadGroup(cs []Count) error {
	if c == nil {
		return nil
	}
	if c.f == nil {
		return fmt.Errorf("Counter is closed")
	}

	// Try to read all with RDPMC. If any fail, fall back to read.
	success := true
	for i := range cs {
		var ok bool
		cs[i], ok = readPMC(c.mmap[i])
		if !ok {
			success = false
			break
		}
	}
	if success {
		return nil
	}

	buf := c.readBuf
	_, err := c.f[0].Read(buf)
	if err != nil {
		return err
	}

	nr := binary.NativeEndian.Uint64(buf[0:])
	if nr != uint64(c.nEvents) {
		return fmt.Errorf("read returned %d events, expected %d", nr, c.nEvents)
	}

	timeEnabled := binary.NativeEndian.Uint64(buf[8:])
	timeRunning := binary.NativeEndian.Uint64(buf[16:])
	for i := 0; i < len(cs) && i < c.nEvents; i++ {
		cs[i].TimeEnabled = timeEnabled
		cs[i].TimeRunning = timeRunning
		cs[i].RawValue = binary.NativeEndian.Uint64(buf[24+i*8:])
		cs[i].scale = c.eventScales[i]
	}
	return nil
}
