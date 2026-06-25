//go:build !windows

package server

import "os/exec"

// hideConsole är en no-op utanför Windows (ingen konsol att dölja).
func hideConsole(cmd *exec.Cmd) {}
