//go:build windows

// daemon_windows.go — detach proses daemon dari sesi terminal (Windows).
//
// Windows tidak punya setsid; padanan terdekat: proses grup baru + tanpa
// console baru, supaya `serve` tidak ikut mati saat terminal pemanggil
// ditutup dan tidak memunculkan jendela cmd baru.
package main

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func detachDaemonProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
