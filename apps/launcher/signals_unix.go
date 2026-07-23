//go:build !windows

package main

import (
	"os"
	"syscall"
)

func launcherSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func gracefulStopSignal() os.Signal {
	return syscall.SIGTERM
}
