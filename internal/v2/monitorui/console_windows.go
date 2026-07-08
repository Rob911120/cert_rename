//go:build windows

package monitorui

import (
	"os/exec"
	"syscall"
)

// hideConsole sätter Windows-processflaggor så att PowerShell körs utan att ett
// konsolfönster blinkar fram: HideWindow (SW_HIDE) + CREATE_NO_WINDOW.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
