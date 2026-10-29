//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// processAlive reports whether a process with the given PID is currently
// running. On Windows os.FindProcess always succeeds, so we attempt to open
// a handle; a permission-denied error still implies the process exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Release()
	return err == nil || errors.Is(err, os.ErrInvalid)
}

// terminateProcess asks the OS to terminate the given PID. Windows has no
// SIGTERM equivalent exposed via the stdlib, so we use the process handle's
// Kill method.
func terminateProcess(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer p.Release()
	return p.Kill() == nil
}

// The kernel creation timestamp distinguishes a live process from a recycled PID.
func processIdentity(pid int) string {
	// PIDs are 32-bit unsigned on Windows; reject anything outside that range
	// before the uint32 conversion to make the overflow impossible.
	if pid <= 1 || uint64(pid) > uint64(^uint32(0)) {
		return ""
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)
	var created, exited, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user) != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", created.HighDateTime, created.LowDateTime)
}
