//go:build !windows

// daemon_unix.go — detach proses daemon dari sesi terminal (Unix/macOS).
package main

import (
	"os/exec"
	"syscall"
)

// detachDaemonProcess memutus proses `serve` dari session terminal pemanggil
// (setsid) supaya daemon tetap hidup setelah client keluar.
func detachDaemonProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
