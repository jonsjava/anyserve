package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/remote"
	"github.com/jonsjava/anyserve/internal/target"
)

// startAgent connects a fleet worker to its manager, if it has one.
func startAgent(ctx context.Context, cmd *cobra.Command, a *app.App, s *api.Server) error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	link := cfg.Fleet.Manager
	if link == nil {
		return nil
	}
	key, err := remote.LoadOrCreateNodeKey(a.Dirs.Config)
	if err != nil {
		return err
	}
	logf := func(f string, args ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", args...) }
	fmt.Fprintf(cmd.OutOrStdout(), "Fleet worker %q of the manager at %s\n", link.Name, link.Addr)
	go func() {
		if err := remote.RunAgent(ctx, *link, key, s.Handler(), logf); err != nil {
			logf("Fleet agent stopped: %v", err)
		}
	}()
	return nil
}

func fleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Run servers on several machines from one manager",
		Long: `One machine, the manager, runs servers on others (workers). Adding a worker is
one command: on the manager, ` + "`anyserve fleet token`" + ` prints it; run it on the new
machine. The worker connects out to the manager's SSH server and stays
connected, so workers need no open ports. The manager's web UI then shows
every worker's servers.

The manager needs remote access on, reachable by the workers:
  anyserve remote enable --listen 0.0.0.0:7022`,
	}
	var addr string
	token := &cobra.Command{
		Use:   "token",
		Short: "Print the one command that adds a machine to this fleet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if addr != "" {
				if err := a.SetManagerAddr(addr); err != nil {
					return err
				}
			}
			maddr, err := a.ManagerAddr()
			if err != nil {
				return err
			}
			key, _, err := remote.LoadOrCreateHostKey(a.Dirs.Config)
			if err != nil {
				return err
			}
			tok, exp, err := a.NewJoinToken(remote.Fingerprint(key.PublicKey()))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Run this on the new machine (works once, until %s):\n\n  %s\n\n", exp.Local().Format(time.Kitchen), remote.JoinCommandFor(maddr, tok))
			fmt.Fprintf(out, "Where Anyserve is already installed: anyserve fleet join %s %s\n", maddr, tok)
			if target.ServeState() != "active" {
				fmt.Fprintln(out, "\nNote: the API service isn't running here, so the worker can't connect yet (`anyserve remote enable` starts it).")
			}
			return nil
		},
	}
	token.Flags().StringVar(&addr, "addr", "", "how workers reach this machine, host:port (remembered)")

	var name string
	var noService bool
	join := &cobra.Command{
		Use:   "join <manager-address> <token>",
		Short: "Join this machine to a manager's fleet",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			cfg, err := a.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.Fleet.Manager != nil {
				return fmt.Errorf("this machine already belongs to the manager at %s (`anyserve fleet leave` first)", cfg.Fleet.Manager.Addr)
			}
			if name == "" {
				h, _ := os.Hostname()
				name = nodeName(h)
			}
			if err := app.ValidNodeName(name); err != nil {
				return err
			}
			key, err := remote.LoadOrCreateNodeKey(a.Dirs.Config)
			if err != nil {
				return err
			}
			hostKey, err := remote.Join(args[0], args[1], name, key)
			if err != nil {
				return err
			}
			cfg, err = a.LoadConfig()
			if err != nil {
				return err
			}
			cfg.Fleet.Manager = &app.ManagerLink{Addr: args[0], HostKey: hostKey, Name: name}
			if err := a.SaveConfig(cfg); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Joined the fleet as %q.\n", name)
			if runtime.GOOS != "linux" || noService {
				fmt.Fprintln(out, "Keep `anyserve serve` running (as a service) so the manager can reach this machine.")
				return nil
			}
			if err := target.InstallServe(a.TargetEnv()); err != nil {
				return fmt.Errorf("joined, but couldn't start the service: %w (run `anyserve serve`)", err)
			}
			fmt.Fprintln(out, "Started the Anyserve service; this machine now shows up on the manager.")
			if !lingering() {
				fmt.Fprintln(out, "To keep it running after you log out: sudo loginctl enable-linger "+os.Getenv("USER"))
			}
			return nil
		},
	}
	join.Flags().BoolVar(&noService, "no-service", false, "don't install the service (run `anyserve serve` yourself, e.g. in a container)")
	join.Flags().StringVar(&name, "name", "", "this machine's name in the fleet (default: its hostname)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List this manager's workers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			cfg, err := a.LoadConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if m := cfg.Fleet.Manager; m != nil {
				fmt.Fprintf(out, "This machine is worker %q of the manager at %s.\n", m.Name, m.Addr)
			}
			if len(cfg.Fleet.Nodes) == 0 {
				fmt.Fprintln(out, "No workers yet. Add one with `anyserve fleet token`.")
				return nil
			}
			status := map[string]api.NodeInfo{}
			if c, err := localAPI(a); err == nil {
				var nodes []api.NodeInfo
				if c.get("/v1/nodes", &nodes) == nil {
					for _, n := range nodes {
						status[n.Name] = n
					}
				}
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NODE\tSTATE\tVERSION\tJOINED")
			for _, n := range cfg.Fleet.Nodes {
				st, ver := "offline", ""
				if s, ok := status[n.Name]; ok && s.Connected {
					st, ver = "connected since "+s.Since.Local().Format(time.DateTime), s.Version
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n.Name, st, ver, n.Joined.Local().Format(time.DateOnly))
			}
			return w.Flush()
		},
	}
	remove := &cobra.Command{
		Use:   "remove <node>",
		Short: "Remove a worker; it can't connect again (its servers keep running)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			// Through the running service, so an open connection is cut too.
			if c, err := localAPI(a); err == nil {
				if err := c.do("DELETE", "/v1/nodes/"+args[0], nil); err != nil {
					return err
				}
			} else if err := a.RemoveNode(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s; it can't connect again. Its servers keep running on it.\n", args[0])
			return nil
		},
	}
	leave := &cobra.Command{
		Use:   "leave",
		Short: "Stop being a worker (this machine's servers keep running)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			cfg, err := a.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.Fleet.Manager == nil {
				return errors.New("this machine isn't in a fleet")
			}
			cfg.Fleet.Manager = nil
			if err := a.SaveConfig(cfg); err != nil {
				return err
			}
			os.Remove(remote.NodeKeyPath(a.Dirs.Config))
			if target.ServeState() == "active" {
				target.RestartServe()
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Left the fleet. Remove this node on the manager too (`anyserve fleet remove`).")
			return nil
		},
	}
	cmd.AddCommand(token, join, list, remove, leave)
	return cmd
}

// nodeName turns a hostname into a node name.
func nodeName(h string) string {
	h = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	var b strings.Builder
	for _, r := range h {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	n := strings.Trim(b.String(), "-")
	if n == "" {
		n = "worker"
	}
	return n
}

func lingering() bool {
	u := os.Getenv("USER")
	_, err := os.Stat("/var/lib/systemd/linger/" + u)
	return err == nil
}
