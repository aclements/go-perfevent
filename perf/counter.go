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

	f []*os.File

	running bool

	nEvents int
	readBuf []byte
}

// OpenCounter returns a new [Counter] that reads values for the given
// [events.Event] or group of Events on the given [Target]. Callers are
// expected to call [Counter.Close] when done with this Counter.
//
// If multiple events are given, they are opened as a group, which means they
// will all be scheduled onto the hardware at the same time.
//
// The counter is initially not running. Call [Counter.Start] to start it.
func OpenCounter(target Target, events ...events.Event) (*Counter, error) {
	if len(events) == 0 {
		return nil, nil
	}

	pid, cpu := target.pidCPU()

	// Open the group leader.
	attr := unix.PerfEventAttr{}
	attr.Size = uint32(unsafe.Sizeof(attr))
	if err := events[0].SetAttrs(&attr); err != nil {
		return nil, err
	}
	attr.Read_format = unix.PERF_FORMAT_TOTAL_TIME_ENABLED |
		unix.PERF_FORMAT_TOTAL_TIME_RUNNING |
		unix.PERF_FORMAT_GROUP
	attr.Bits = unix.PerfBitDisabled

	// TODO: Allow setting flags that make sense.

	var c Counter
	c.target = target
	c.nEvents = len(events)

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
	defer func() {
		if !success {
			for _, f := range c.f {
				f.Close()
			}
		}
	}()

	// Open other events.
	for _, event := range events[1:] {
		attr = unix.PerfEventAttr{}
		attr.Size = uint32(unsafe.Sizeof(attr))
		if err := event.SetAttrs(&attr); err != nil {
			return nil, err
		}
		attr.Bits = unix.PerfBitDisabled

		fd2, err := unix.PerfEventOpen(&attr, pid, cpu, fd, unix.PERF_FLAG_FD_CLOEXEC)
		if err != nil {
			return nil, err
		}

		// I'm honestly not sure what this FD is for, but we shouldn't close it,
		// so we hold on to it.
		c.f = append(c.f, os.NewFile(uintptr(fd2), "<perf-event>"))
	}

	// Allocate a large enough read buffer.
	c.readBuf = make([]byte, 3*8+len(events)*8)

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
}

// Value returns the value of Count, scaled to account for time the counter was
// descheduled.
func (c Count) Value() uint64 {
	if c.TimeEnabled == c.TimeRunning {
		// Common case: it was running the whole time.
		return c.RawValue
	}
	if c.TimeRunning == 0 {
		// Avoid divide by zero.
		return 0
	}
	return uint64(float64(c.RawValue) * (float64(c.TimeEnabled) / float64(c.TimeRunning)))
}

// ReadOne returns the current value of the first event in c. For counters that
// only have a single Event, this is faster and more ergonomic than
// [Counter.ReadGroup].
func (c *Counter) ReadOne() (Count, error) {
	// TODO: Use RDPMC when possible.
	if c == nil {
		return Count{}, nil
	}

	var cs [1]Count
	if err := c.ReadGroup(cs[:]); err != nil {
		return Count{}, err
	}
	return cs[0], nil
}

// ReadGroup returns the current value of all events in c.
func (c *Counter) ReadGroup(cs []Count) error {
	if c == nil {
		return nil
	}
	if c.f == nil {
		return fmt.Errorf("Counter is closed")
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
	}
	return nil
}
