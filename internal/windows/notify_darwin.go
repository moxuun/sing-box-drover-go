//go:build darwin

package windows

import "os/exec"

const displayAlertScript = `on run argv
display alert (item 1 of argv) message (item 2 of argv) as critical buttons {"OK"} default button "OK"
end run`

// ShowError gives GUI builds a visible diagnostic when startup fails before
// the status item exists. Console builds still print the same error in main.
func ShowError(title, message string) {
	_ = exec.Command("/usr/bin/osascript", "-e", displayAlertScript, "--", title, message).Run()
}
