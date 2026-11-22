package supervise

import "syscall"

// setDeathSignal makes the kernel stop the server if the supervisor dies,
// so a crashed supervisor never leaves an orphaned server holding the port.
func setDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGTERM }
