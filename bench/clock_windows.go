//go:build windows

package main

import (
	"sync"
	"syscall"
	"unsafe"
)

// The Windows wall clock exposed through time.Now() has ~500µs granularity
// on some machines, which is too coarse for microsecond workloads. QPC is
// monotonic and resolves to ~100ns; it is used only for per-operation
// samples, never for wall-clock timestamps.
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	qpcProc  = kernel32.NewProc("QueryPerformanceCounter")
	qpfProc  = kernel32.NewProc("QueryPerformanceFrequency")

	qpcOnce sync.Once
	qpcFreq int64
)

func clockNow() int64 {
	qpcOnce.Do(func() {
		qpfProc.Call(uintptr(unsafe.Pointer(&qpcFreq)))
		if qpcFreq <= 0 {
			qpcFreq = 1
		}
	})
	var ticks int64
	qpcProc.Call(uintptr(unsafe.Pointer(&ticks)))
	return int64(float64(ticks) * 1e9 / float64(qpcFreq))
}
