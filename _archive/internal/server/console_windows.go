//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// hideConsole sätter Windows-processflaggor så att PowerShell körs utan att ett
// konsolfönster blinkar fram: HideWindow (SW_HIDE) + CREATE_NO_WINDOW. Det
// undviker både den synliga rutan och att den stjäl fokus från Monitor-rutinen.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
