//go:build !windows

package supervise

import (
	"os/signal"
	"syscall"
)

func ignoreInterrupt() { signal.Ignore(syscall.SIGINT, syscall.SIGTERM) }
