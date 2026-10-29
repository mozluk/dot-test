//go:build !windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// processAlive reports whether a process with the given PID is currently
// running. On Unix this is done by sending signal 0, which performs error
// checking without actually delivering a signal.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// terminateProcess sends SIGTERM to the given PID. It reports whether the
// signal was delivered successfully.
func terminateProcess(pid int) bool {
	return syscall.Kill(pid, syscall.SIGTERM) == nil
}

// Query the process start time and command without invoking a shell.
func processIdentity(pid int) string {
	if pid <= 1 {
		return ""
	}
	data, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
