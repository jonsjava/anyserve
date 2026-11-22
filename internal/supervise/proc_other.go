//go:build !linux && !windows

package supervise

import "syscall"

func setDeathSignal(*syscall.SysProcAttr) {}
