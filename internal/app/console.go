package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/rcon"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/supervise"
	"github.com/jonsjava/anyserve/internal/target"
)

// ConsoleKind says how Anyserve reaches a server's console.
type ConsoleKind string

const (
	// ConsoleStdin types into the server's own console through the
	// supervisor. Replies only show up in the server's output.
	ConsoleStdin ConsoleKind = "stdin"
	// ConsoleRCON uses the server's remote console, which replies.
	ConsoleRCON ConsoleKind = "rcon"
	// ConsoleExec runs a program in one of a stack's containers.
	ConsoleExec ConsoleKind = "exec"
)

// ConsoleInfo describes an instance's console, for `console --info`.
type ConsoleInfo struct {
	Kind ConsoleKind
	// For RCON.
	Protocol string
	Port     int
	Public   bool
	Password string
	// For exec.
	Service     string
	Command     []string
	Description string
}

// ConsoleDetails reports how to reach an instance's console.
func (a *App) ConsoleDetails(name string) (ConsoleInfo, error) {
	in, d, _, err := a.load(name)
	if err != nil {
		return ConsoleInfo{}, err
	}
	return consoleInfo(in, d)
}

func consoleInfo(in *instance.Instance, d *spec.Definition) (ConsoleInfo, error) {
	ctx, err := in.Context(d, "linux")
	if err != nil {
		return ConsoleInfo{}, err
	}
	switch {
	case d.Console.Exec != nil:
		x := d.Console.Exec
		info := ConsoleInfo{Kind: ConsoleExec, Service: x.Service, Description: x.Description}
		for i, c := range x.Command {
			v, err := spec.Render(fmt.Sprintf("console.exec.command[%d]", i), c, ctx)
			if err != nil {
				return ConsoleInfo{}, err
			}
			info.Command = append(info.Command, v)
		}
		return info, nil
	case d.IsStack():
		return ConsoleInfo{}, fmt.Errorf("%s doesn't define a console", d.Name)
	case d.Console.RCON != nil:
		r := d.Console.RCON
		pw, err := spec.Render("console.rcon.password", r.Password, ctx)
		if err != nil {
			return ConsoleInfo{}, err
		}
		pt, _ := d.Port(r.Port)
		return ConsoleInfo{Kind: ConsoleRCON, Protocol: r.Protocol, Port: ctx.Ports[r.Port], Public: pt.Public, Password: pw}, nil
	}
	return ConsoleInfo{Kind: ConsoleStdin}, nil
}

// ConsoleOptions picks how a console command is delivered.
type ConsoleOptions struct {
	// Stdin forces the server's own console even when RCON is available.
	Stdin bool
}

func (a *App) consoleFor(name string, o ConsoleOptions) (*instance.Instance, *spec.Definition, target.Target, ConsoleInfo, error) {
	in, d, t, err := a.load(name)
	if err != nil {
		return nil, nil, nil, ConsoleInfo{}, err
	}
	info, err := consoleInfo(in, d)
	if err != nil {
		return nil, nil, nil, ConsoleInfo{}, err
	}
	if o.Stdin && info.Kind == ConsoleRCON {
		info = ConsoleInfo{Kind: ConsoleStdin}
	}
	if o.Stdin && info.Kind == ConsoleExec {
		return nil, nil, nil, ConsoleInfo{}, errors.New("stacks have no server console; leave out --stdin")
	}
	if st := t.Status(in).State; st != "running" && st != "partly running" {
		return nil, nil, nil, ConsoleInfo{}, fmt.Errorf("%s isn't running (%s); start it with `anyserve start %s`", name, st, name)
	}
	return in, d, t, info, nil
}

func (a *App) dialRCON(info ConsoleInfo) (rcon.Client, error) {
	c, err := rcon.Dial(rcon.Options{
		Protocol: info.Protocol,
		Addr:     net.JoinHostPort("127.0.0.1", strconv.Itoa(info.Port)),
		Password: info.Password,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("%w (if the server is still starting, try again shortly, or use --stdin)", err)
	}
	return c, nil
}

// ConsoleCommand runs one console command and returns what the server
// printed in reply.
func (a *App) ConsoleCommand(name, line string, o ConsoleOptions) (string, error) {
	in, _, _, info, err := a.consoleFor(name, o)
	if err != nil {
		return "", err
	}
	switch info.Kind {
	case ConsoleExec:
		var out strings.Builder
		err := target.DockerExec(in, info.Service, info.Command, false, strings.NewReader(line+"\n"), &out, &out)
		return out.String(), err
	case ConsoleRCON:
		c, err := a.dialRCON(info)
		if err != nil {
			return "", err
		}
		defer c.Close()
		return c.Exec(line)
	}
	// The server's own console doesn't reply directly, so show what it
	// prints right after the command.
	logPath := filepath.Join(in.LogDir(), "server.log")
	start := fileSize(logPath)
	if err := supervise.Send(in.ConsoleSocket(), line); err != nil {
		return "", err
	}
	time.Sleep(1500 * time.Millisecond)
	return readFrom(logPath, start), nil
}

// ConsoleSession is an interactive console: what the user types goes to the
// server, and replies (or, for the server's own console, all its output)
// are shown until input ends.
func (a *App) ConsoleSession(ctx context.Context, name string, o ConsoleOptions, stdin io.Reader, out io.Writer, tty bool) error {
	in, d, _, info, err := a.consoleFor(name, o)
	if err != nil {
		return err
	}
	switch info.Kind {
	case ConsoleExec:
		what := info.Description
		if what == "" {
			what = strings.Join(info.Command, " ")
		}
		fmt.Fprintf(out, "Opening %s in the %s container. Exit it the usual way to return.\n", what, info.Service)
		return target.DockerExec(in, info.Service, info.Command, tty, stdin, out, out)
	case ConsoleRCON:
		c, err := a.dialRCON(info)
		if err != nil {
			return err
		}
		defer c.Close()
		fmt.Fprintf(out, "Connected to %s over %s RCON. Type commands; press Ctrl-D to leave (the server keeps running).\n", d.Name, info.Protocol)
		return repl(ctx, stdin, out, func(line string) error {
			reply, err := c.Exec(line)
			if reply != "" {
				fmt.Fprintln(out, strings.TrimRight(reply, "\n"))
			}
			return err
		})
	}
	logPath := filepath.Join(in.LogDir(), "server.log")
	fmt.Fprintf(out, "Attached to %s's console. Server output follows; type commands; press Ctrl-D to leave (the server keeps running).\n", d.Name)
	tailCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go tail(tailCtx, logPath, fileSize(logPath), out)
	return repl(ctx, stdin, out, func(line string) error {
		return supervise.Send(in.ConsoleSocket(), line)
	})
}

// repl feeds input lines to send until input ends or ctx is cancelled.
func repl(ctx context.Context, stdin io.Reader, out io.Writer, send func(string) error) error {
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := send(line); err != nil {
				fmt.Fprintf(out, "error: %v\n", err)
				if errors.Is(err, rcon.ErrAuth) || isConnErr(err) {
					return err
				}
			}
		}
	}
}

func isConnErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed")
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func readFrom(p string, off int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	return string(b)
}

// tail copies new log output to out until ctx ends.
func tail(ctx context.Context, p string, off int64, out io.Writer) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
		size := fileSize(p)
		if size < off {
			off = 0 // the log was rotated
		}
		if size > off {
			io.WriteString(out, readFrom(p, off))
			off = size
		}
	}
}

// sendLine delivers one console line to a running server: over RCON when
// the definition declares it (many servers ignore their standard input),
// otherwise to its console socket. Used for chat broadcasts and saves.
func (a *App) sendLine(in *instance.Instance, d *spec.Definition, line string) error {
	info, err := consoleInfo(in, d)
	if err != nil {
		return err
	}
	if info.Kind != ConsoleRCON {
		return supervise.Send(in.ConsoleSocket(), line)
	}
	c, err := a.dialRCON(info)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Exec(line)
	return err
}
