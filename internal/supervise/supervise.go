// Package supervise runs a server process in the foreground and gives every
// platform the same controls: a console socket for sending commands to the
// server's stdin, a clean stop that types the server's own stop command
// before resorting to signals, and a log file.
//
// Service managers (systemd, Windows services, launchd) and containers all
// run `anyserve run <name>`, which ends up here, so stopping behaves the same
// everywhere.
package supervise

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Options configures one supervised run.
type Options struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	// StopCommand is written to stdin to stop the server cleanly.
	StopCommand string
	// StopFunc, if set, sends StopCommand another way (such as RCON)
	// instead of writing it to stdin.
	StopFunc    func() error
	StopTimeout time.Duration
	// Socket is the console socket path. Empty disables it.
	Socket string
	// LogDir receives server.log. Empty disables the log file.
	LogDir string
	// Stdout receives the server's output as well (e.g. the journal).
	Stdout io.Writer
	// Log receives the supervisor's own messages.
	Log func(format string, a ...any)
}

// Run starts the server and blocks until it exits. A SIGINT or SIGTERM
// (or ctx being cancelled) triggers a clean stop.
func Run(ctx context.Context, o Options) error {
	if o.StopTimeout <= 0 {
		o.StopTimeout = 60 * time.Second
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}

	out := o.Stdout
	if o.LogDir != "" {
		lf, err := openLog(o.LogDir)
		if err != nil {
			return err
		}
		defer lf.Close()
		out = io.MultiWriter(o.Stdout, lf)
	}
	logw := &lockedWriter{w: out}

	cmd := exec.Command(o.Command, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	cmd.Stdout = logw
	cmd.Stderr = logw
	setProcAttrs(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// On Linux the death signal is tied to the thread that started the
	// child, so keep this goroutine on one thread for the server's life.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", o.Command, err)
	}
	o.Log("Started %s (pid %d)", filepath.Base(o.Command), cmd.Process.Pid)

	var stdinMu sync.Mutex
	send := func(line string) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, err := io.WriteString(stdin, line+"\n")
		return err
	}

	if o.Socket != "" {
		ln, err := listenConsole(o.Socket)
		if err != nil {
			o.Log("Console socket unavailable: %v", err)
		} else {
			defer ln.Close()
			defer os.Remove(o.Socket)
			go serveConsole(ln, send, o.Log)
		}
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	select {
	case err := <-exited:
		return exitError(err)
	case s := <-sigs:
		o.Log("Received %v; stopping the server", s)
	case <-ctx.Done():
		o.Log("Stopping the server")
	}

	// Clean stop: type the stop command, then interrupt, then kill.
	if o.StopCommand != "" {
		deliver := send
		if o.StopFunc != nil {
			deliver = func(string) error { return o.StopFunc() }
		}
		if err := deliver(o.StopCommand); err != nil {
			o.Log("Couldn't send %q: %v", o.StopCommand, err)
		} else {
			o.Log("Sent %q; waiting up to %s", o.StopCommand, o.StopTimeout)
		}
	} else {
		interrupt(cmd)
		o.Log("Sent interrupt; waiting up to %s", o.StopTimeout)
	}
	select {
	case err := <-exited:
		return stoppedError(err)
	case <-time.After(o.StopTimeout):
	case <-sigs:
		o.Log("Second signal; not waiting any longer")
	}
	if o.StopCommand != "" {
		interrupt(cmd)
		o.Log("Server didn't stop; sent interrupt")
		select {
		case err := <-exited:
			return stoppedError(err)
		case <-time.After(15 * time.Second):
		}
	}
	o.Log("Server didn't stop; killing it")
	kill(cmd)
	<-exited
	return errors.New("server had to be killed")
}

// Send writes one console command to a running server's socket.
func Send(socket, line string) error {
	if strings.ContainsAny(line, "\r\n") {
		return errors.New("console commands must be a single line")
	}
	c, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fmt.Errorf("server console isn't reachable (is the server running?): %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintln(c, line); err != nil {
		return err
	}
	resp, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if resp = strings.TrimSpace(resp); resp != "ok" {
		return errors.New(resp)
	}
	return nil
}

func listenConsole(p string) (net.Listener, error) {
	// A stale socket from a crash would block Listen.
	os.Remove(p)
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	// Only the owner may type into the server console.
	if err := os.Chmod(p, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func serveConsole(ln net.Listener, send func(string) error, log func(string, ...any)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetDeadline(time.Now().Add(10 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(c, 4096)).ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if err := send(line); err != nil {
				fmt.Fprintf(c, "error: %v\n", err)
				return
			}
			log("Console: %s", line)
			fmt.Fprintln(c, "ok")
		}(c)
	}
}

func openLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "server.log")
	// Keep the last five runs' logs.
	for i := 4; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", p, i), fmt.Sprintf("%s.%d", p, i+1))
	}
	os.Rename(p, p+".1")
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func exitError(err error) error {
	if err == nil {
		return errors.New("server exited unexpectedly")
	}
	return fmt.Errorf("server exited: %w", err)
}

// stoppedError treats any exit after a stop request as success, since many
// servers exit non-zero when told to quit.
func stoppedError(error) error { return nil }
