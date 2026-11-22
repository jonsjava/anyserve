//go:build !windows

package supervise

import (
	"os/exec"
	"syscall"
)

// setProcAttrs puts the server in its own process group, so Ctrl+C in a
// terminal reaches only the supervisor, which then stops the server cleanly.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setDeathSignal(cmd.SysProcAttr)
}

func interrupt(cmd *exec.Cmd) {
	// Signal the whole group so helper processes stop too.
	syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

func kill(cmd *exec.Cmd) {
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
