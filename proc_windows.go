//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps a helper program from opening a console window.
func hideConsole(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
