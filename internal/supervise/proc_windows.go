package supervise

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// Windows has no SIGINT for other processes; servers are asked to stop
// through their console command, and killed if that fails.
func interrupt(cmd *exec.Cmd) {}

func kill(cmd *exec.Cmd) { cmd.Process.Kill() }
