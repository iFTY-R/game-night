//go:build windows

package main

import "os"

func launcherSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func gracefulStopSignal() os.Signal {
	return os.Interrupt
}
