package server

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// nativeFolderDialog öppnar en plattformsspecifik mappväljare och returnerar
// vald path eller tom sträng om användaren avbröt.
func nativeFolderDialog(prompt string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`POSIX path of (choose folder with prompt %q)`, prompt)
		out, err := exec.Command("osascript", "-e", script).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimRight(string(out), "/\n"), nil
	case "windows":
		ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms;
$d = New-Object System.Windows.Forms.FolderBrowserDialog;
$d.Description = %q;
if ($d.ShowDialog() -eq 'OK') { Write-Output $d.SelectedPath }`, prompt)
		out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	default:
		out, err := exec.Command("zenity", "--file-selection", "--directory",
			"--title="+prompt).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	}
}
