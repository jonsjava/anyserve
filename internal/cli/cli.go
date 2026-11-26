// Package cli implements the anyserve command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/jonsjava/anyserve/internal/version"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/rcon"
	"github.com/jonsjava/anyserve/internal/registry"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/target"
)

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	root := newRoot()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	return 0
}

func interactive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

func newApp(cmd *cobra.Command) (*app.App, error) {
	return app.New(cmd.OutOrStdout())
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "anyserve",
		Short: "Host game servers and other services on any platform",
		Long: `Anyserve installs and runs servers from a community registry of service
definitions, on Linux, Windows or macOS, as a native service, a container or
on Kubernetes.

Run anyserve with no arguments for a guided install.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !interactive() {
				return cmd.Help()
			}
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			return runWizard(cmd.Context(), a)
		},
	}
	root.AddCommand(
		availableCmd(), infoCmd(), installCmd(), listCmd(), statusCmd(),
		startCmd(), stopCmd(), restartCmd(), logsCmd(), updateCmd(),
		configCmd(), portsCmd(), consoleCmd(), rconCmd(), sayCmd(),
		backupCmd(), backupsCmd(), restoreCmd(), removeCmd(), autoUpdateCmd(), serveCmd(),
		usersCmd(), remoteCmd(), proxyCmd(), portForwardCmd(), httpsCmd(), dnsCmd(), routerCmd(), upgradeCmd(), fleetCmd(), reserveCmd(),
		refreshCmd(), registryCmd(),
		steamCmd(), targetsCmd(), runCmd(), versionCmd(),
	)
	return root
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func availableCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "available [search]",
		Aliases: []string{"search"},
		Short:   "List services you can install",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			entries, warnings, err := a.Catalog()
			if err != nil {
				return err
			}
			printWarnings(cmd, warnings)
			q := ""
			if len(args) == 1 {
				q = strings.ToLower(args[0])
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tNAME\tPLATFORMS\tDESCRIPTION")
			n := 0
			for _, e := range entries {
				d := e.Def
				if q != "" && !strings.Contains(strings.ToLower(d.ID+" "+d.Name+" "+d.Description), q) {
					continue
				}
				id := d.ID
				if e.Registry != registry.MainName {
					id = e.Registry + "/" + d.ID
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, d.Name, platformList(d), d.Description)
				n++
			}
			w.Flush()
			if n == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching services.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "\n* runs natively on this machine. Docker services need the docker target.")
			}
			return nil
		},
	}
}

func platformList(d *spec.Definition) string {
	if d.IsStack() {
		return "Docker"
	}
	var out []string
	for _, p := range d.Platforms {
		name := map[string]string{"linux": "Linux", "windows": "Windows", "darwin": "macOS"}[p]
		if p == runtime.GOOS {
			name += "*"
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <service>",
		Short: "Show a service's settings, ports and notes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			e, err := a.Find(args[0])
			if err != nil {
				return err
			}
			d := e.Def
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s (%s, from registry %s, definition %s)\n%s\n", d.Name, d.ID, e.Registry, d.Version, d.Description)
			if d.Homepage != "" {
				fmt.Fprintf(out, "Homepage: %s\n", d.Homepage)
			}
			fmt.Fprintf(out, "Runs on: %s\n", platformList(d))
			switch {
			case d.Install.SteamCMD != nil:
				fmt.Fprintf(out, "Installs from: SteamCMD (app %d)\n", d.Install.SteamCMD.AppID)
			case d.Install.Archive != nil:
				fmt.Fprintf(out, "Installs from: %s\n", d.Install.Archive.URL)
			case d.IsStack():
				fmt.Fprintln(out, "Runs as a stack of containers (docker target):")
				for _, sv := range d.Stack.Services {
					fmt.Fprintf(out, "  %-10s %s\n", sv.Name, sv.Image)
				}
			}
			fmt.Fprintln(out, "\nSettings (change with --set key=value):")
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			for _, s := range d.Settings {
				def := s.DefaultString()
				switch {
				case s.Generate:
					def = "(generated)"
				case s.Secret && def != "":
					def = "(hidden)"
				case def == "" && s.Optional:
					def = "(empty)"
				}
				extra := ""
				if s.Type == "choice" {
					var vals []string
					for _, c := range s.Choices {
						vals = append(vals, c.Value+"="+c.Label)
					}
					extra = " [" + strings.Join(vals, ", ") + "]"
				}
				if s.Type == "folder" {
					extra = " [a folder on this machine: " + strings.Join(folderMounts(d, s.Key), "; ") + "]"
				}
				fmt.Fprintf(w, "  %s\t%s\t%s%s\n", s.Key, def, s.Prompt, extra)
			}
			w.Flush()
			switch {
			case d.Console.RCON != nil:
				fmt.Fprintf(out, "\nConsole: %s RCON on port %q, plus `anyserve console`\n", d.Console.RCON.Protocol, d.Console.RCON.Port)
			case d.Console.Exec != nil:
				fmt.Fprintf(out, "\nConsole: %s (`anyserve console`)\n", d.Console.Exec.Description)
			case !d.IsStack():
				fmt.Fprintln(out, "\nConsole: the server's own console, through `anyserve console` on this machine")
			}
			fmt.Fprintln(out, "\nPorts (change with --port name=number):")
			ports, _ := d.ResolvePorts(nil)
			fmt.Fprint(out, app.FormatPorts(d, ports))
			if d.LinuxGSM != nil {
				fmt.Fprintf(out, "\nLinuxGSM: %s", d.LinuxGSM.Status)
				if d.LinuxGSM.Name != "" {
					fmt.Fprintf(out, " (%s)", d.LinuxGSM.Name)
				}
				fmt.Fprintln(out)
			}
			if len(d.Notes) > 0 {
				fmt.Fprintln(out, "\nNotes:")
				for _, n := range d.Notes {
					fmt.Fprintf(out, "  - %s\n", n)
				}
			}
			return nil
		},
	}
}

func parseSets(sets []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q: use key=value", s)
		}
		out[k] = v
	}
	return out, nil
}

func parsePorts(ports []string) (map[string]int, error) {
	out := map[string]int{}
	for _, p := range ports {
		k, v, ok := strings.Cut(p, "=")
		n, err := strconv.Atoi(v)
		if !ok || k == "" || err != nil {
			return nil, fmt.Errorf("--port %q: use name=number", p)
		}
		out[k] = n
	}
	return out, nil
}

func installCmd() *cobra.Command {
	var (
		req         app.InstallRequest
		sets, ports []string
		yes         bool
	)
	cmd := &cobra.Command{
		Use:   "install <service>",
		Short: "Install a service as a new instance",
		Example: `  anyserve install terraria --name friends --set max_players=16 --start
  anyserve install rust --port game=28025 --set server_name="Weekend wipe"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			req.Service = args[0]
			if req.Settings, err = parseSets(sets); err != nil {
				return err
			}
			if req.Ports, err = parsePorts(ports); err != nil {
				return err
			}
			plan, err := a.PlanInstall(req)
			if err != nil {
				return err
			}
			printPlan(cmd, a, plan)
			if !yes && interactive() {
				ok, err := confirm("Install now?")
				if err != nil || !ok {
					return errors.New("cancelled")
				}
			}
			ctx, cancel := signalContext()
			defer cancel()
			in, err := a.Install(ctx, plan)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nNext:\n  anyserve start %s\n  anyserve logs %s -f\n  anyserve ports %s\n", in.Name, in.Name, in.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Name, "name", "", "instance name (default: the service id)")
	cmd.Flags().StringVar(&req.Target, "target", "", "where to run it: "+strings.Join(target.Names(), ", ")+" (default: "+target.Default()+")")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "setting as key=value (repeatable; see `anyserve info <service>`)")
	cmd.Flags().StringArrayVar(&ports, "port", nil, "port as name=number (repeatable)")
	cmd.Flags().StringVar(&req.ServerDir, "dir", "", "put the server files here instead of the instance directory")
	cmd.Flags().BoolVar(&req.Start, "start", false, "start the server after installing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&req.SteamOutput, "steam-output", false, "show SteamCMD's full output")
	cmd.Flags().StringVar(&req.Domain, "domain", "", "give the instance a domain: HTTPS for apps, the shared port for Minecraft, a DNS name otherwise (`anyserve dns`)")
	cmd.Flags().StringVar(&req.DomainPort, "domain-port", "", "which port the domain reaches (default: the app's web port)")
	cmd.Flags().BoolVar(&req.Isolate, "isolate", false, "run a native server in a container that sees only its own folder (Linux, Docker)")
	cmd.Flags().StringArrayVar(&req.Expose, "expose", nil, "open a private port (such as a web UI) to the network instead of this machine only (stacks; repeatable)")
	return cmd
}

func printPlan(cmd *cobra.Command, a *app.App, p *app.Plan) {
	out := cmd.OutOrStdout()
	d := p.Entry.Def
	fmt.Fprintf(out, "Service:  %s (%s)\nName:     %s\nTarget:   %s\n", d.Name, d.ID, p.Req.Name, p.Target.Name())
	fmt.Fprintln(out, "Settings:")
	for _, s := range d.Settings {
		fmt.Fprintf(out, "  %s = %s\n", s.Key, displayValue(s, p.Settings[s.Key]))
	}
	fmt.Fprintln(out, "Ports:")
	fmt.Fprint(out, app.FormatPortsShared(d, p.Ports, a.SharedPorts(d, p.Req.Domain, p.Req.DomainPort)))
	for _, m := range p.Moved {
		fmt.Fprintf(out, "Note: %s\n", m)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", w)
	}
	for _, n := range d.Notes {
		fmt.Fprintf(out, "Note: %s\n", n)
	}
}

func displayValue(s spec.Setting, v any) string {
	str := fmt.Sprint(v)
	if s.Secret && str != "" {
		return "(hidden)"
	}
	if str == "" {
		return "(empty)"
	}
	if s.Type == "choice" {
		for _, c := range s.Choices {
			if c.Value == str {
				return str + " (" + c.Label + ")"
			}
		}
	}
	return str
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List installed instances",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			lines, broken, err := a.Statuses()
			if err != nil {
				return err
			}
			if len(lines) == 0 && len(broken) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No instances yet. Run `anyserve` for a guided install, or `anyserve available` to see what you can install.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSERVICE\tTARGET\tSTATE\tPORTS\tNOTE")
			for _, l := range lines {
				ports, _ := l.Def.ResolvePorts(l.Instance.Ports)
				var ps []string
				for _, p := range l.Def.Ports {
					if p.Public {
						ps = append(ps, fmt.Sprintf("%d/%s", ports[p.Name], p.Protocol))
					}
				}
				note := l.Status.Detail
				if l.Upgrade != "" {
					note = strings.TrimSpace(note + " definition " + l.Upgrade + " available (anyserve update)")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", l.Instance.Name, l.Instance.Service, l.Instance.Target, l.Status.State, strings.Join(ps, " "), note)
			}
			names := make([]string, 0, len(broken))
			for n := range broken {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Fprintf(w, "%s\t?\t?\tbroken\t\t%v\n", n, broken[n])
			}
			return w.Flush()
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show one instance's state and details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			l, err := a.Status(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			in := l.Instance
			fmt.Fprintf(out, "Name:        %s\nService:     %s (definition %s, registry %s)\nTarget:      %s\nState:       %s %s\n", in.Name, l.Def.Name, in.DefinitionVersion, in.Registry, in.Target, l.Status.State, l.Status.Detail)
			fmt.Fprintf(out, "Server files: %s\nLogs:        %s\nInstalled:   %s\nUpdated:     %s\n", in.ServerDir, in.LogDir(), in.Installed.Local().Format(time.DateTime), in.Updated.Local().Format(time.DateTime))
			if l.Upgrade != "" {
				fmt.Fprintf(out, "Update:      definition %s is available; run `anyserve update %s`\n", l.Upgrade, in.Name)
			}
			fmt.Fprintf(out, "Auto-update: %s\n", app.Policy(in))
			printStats(out, a, in.Name)
			return nil
		},
	}
}

// printStats adds resource use to `status`: two readings a second apart,
// for the CPU rate.
func printStats(out io.Writer, a *app.App, name string) {
	s1, err := a.Stats(name)
	if err != nil || !s1.Running {
		return
	}
	time.Sleep(time.Second)
	s2, err := a.Stats(name)
	if err != nil || !s2.Running {
		return
	}
	if !s2.Started.IsZero() {
		fmt.Fprintf(out, "Uptime:      %s (since %s)\n", time.Since(s2.Started).Round(time.Second), s2.Started.Local().Format(time.DateTime))
	}
	if dt := s2.At.Sub(s1.At).Seconds(); dt > 0 && s2.CPU >= s1.CPU {
		fmt.Fprintf(out, "CPU:         %.1f%% (100%% is one of %d cores)\n", float64(s2.CPU-s1.CPU)/1e9/dt*100, s2.Cores)
	}
	fmt.Fprintf(out, "Memory:      %s of %s\n", byteSize(int64(s2.Memory)), byteSize(int64(s2.MemoryMax)))
	fmt.Fprintf(out, "Disk:        %s\n", byteSize(s2.Disk))
	if s2.Net {
		fmt.Fprintf(out, "Network:     %s in, %s out since it started\n", byteSize(int64(s2.NetRx)), byteSize(int64(s2.NetTx)))
	}
}

func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func simpleCmd(use, short string, f func(a *app.App, name string) error, done string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := f(a, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), done+"\n", args[0])
			return nil
		},
	}
}

func startCmd() *cobra.Command {
	return simpleCmd("start", "Start an instance", (*app.App).Start, "Started %s. Follow its output with `anyserve logs -f`.")
}

func stopCmd() *cobra.Command {
	return simpleCmd("stop", "Stop an instance cleanly", (*app.App).Stop, "Stopped %s.")
}

func restartCmd() *cobra.Command {
	var warn time.Duration
	cmd := &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.Restart(args[0], warn); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Restarted %s.\n", args[0])
			return nil
		},
	}
	cmd.Flags().DurationVar(&warn, "warn", 0, "warn players this long before restarting, e.g. 5m")
	return cmd
}

func logsCmd() *cobra.Command {
	var (
		lines  int
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show an instance's output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			return a.Logs(args[0], lines, follow)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "number of lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep showing new output")
	return cmd
}

func updateCmd() *cobra.Command {
	var (
		warn        time.Duration
		steamOutput bool
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update the server files and definition, restarting if it was running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			if err := a.Update(ctx, args[0], warn, steamOutput); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated %s.\n", args[0])
			return nil
		},
	}
	cmd.Flags().DurationVar(&warn, "warn", 0, "warn players this long before stopping, e.g. 10m")
	cmd.Flags().BoolVar(&steamOutput, "steam-output", false, "show SteamCMD's full output")
	return cmd
}

func configCmd() *cobra.Command {
	var (
		sets, ports        []string
		expose, unexpose   []string
		isolate, noIsolate bool
		domain, domainPort string
		noDomain           bool
		showSecrets        bool
	)
	cmd := &cobra.Command{
		Use:   "config <name>",
		Short: "Show or change an instance's settings and ports",
		Example: `  anyserve config friends
  anyserve config friends --set max_players=24 --port game=7778
  anyserve config vault --expose http   # reachable from other computers`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if len(sets) > 0 || len(ports) > 0 || len(expose) > 0 || len(unexpose) > 0 || isolate || noIsolate || domain != "" || domainPort != "" || noDomain {
				s, err := parseSets(sets)
				if err != nil {
					return err
				}
				p, err := parsePorts(ports)
				if err != nil {
					return err
				}
				ch := app.Changes{Settings: s, Ports: p, Expose: expose, Unexpose: unexpose}
				if isolate || noIsolate {
					v := isolate && !noIsolate
					ch.Isolate = &v
				}
				if noDomain {
					empty := ""
					ch.Domain = &empty
				} else if domain != "" {
					ch.Domain = &domain
				}
				if domainPort != "" {
					ch.DomainPort = &domainPort
				}
				if err := a.Reconfigure(args[0], ch); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Updated %s's configuration.\n", args[0])
				return nil
			}
			in, d, err := a.Store.Load(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Settings:")
			for _, s := range d.Settings {
				v := in.Settings[s.Key]
				shown := displayValue(s, v)
				if showSecrets && s.Secret {
					shown = v
				}
				fmt.Fprintf(out, "  %s = %s\n", s.Key, shown)
			}
			ps, _ := d.ResolvePorts(in.Ports)
			fmt.Fprintln(out, "Ports:")
			fmt.Fprint(out, app.FormatPortsShared(d, ps, a.SharedPorts(d, in.Domain, in.DomainPort)))
			if len(in.Expose) > 0 {
				fmt.Fprintf(out, "Opened to the network with --expose: %s\n", strings.Join(in.Expose, ", "))
			}
			if in.Domain != "" {
				shown := in.Domain
				if t, err := app.TargetFor(d, in.DomainPort); err == nil {
					switch t.Kind {
					case spec.RouteHTTP, spec.RouteTLS:
						shown = "https://" + in.Domain
					case spec.RouteMinecraft:
						shown = in.Domain + " (through the Minecraft router)"
					default:
						shown = in.Domain + " (DNS name only)"
					}
				}
				fmt.Fprintf(out, "Domain:    %s; `anyserve dns %s` lists the DNS records\n", shown, in.Name)
			}
			if in.Isolation != "" {
				fmt.Fprintf(out, "Isolation: %s (sees only %s)\n", in.Isolation, in.ServerDir)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&expose, "expose", nil, "open a private port to the network instead of this machine only (stacks; repeatable)")
	cmd.Flags().StringVar(&domain, "domain", "", "give the instance a domain: HTTPS for apps, the shared port for Minecraft, a DNS name otherwise (`anyserve dns`)")
	cmd.Flags().StringVar(&domainPort, "domain-port", "", "which port the domain reaches (default: the web port)")
	cmd.Flags().BoolVar(&noDomain, "no-domain", false, "stop serving the app at its domain")
	cmd.Flags().BoolVar(&isolate, "isolate", false, "run this native server in a container that sees only its own folder")
	cmd.Flags().BoolVar(&noIsolate, "no-isolate", false, "run it directly on the host again")
	cmd.Flags().StringArrayVar(&unexpose, "unexpose", nil, "make an exposed port private again (repeatable)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "change a setting, key=value (repeatable)")
	cmd.Flags().StringArrayVar(&ports, "port", nil, "change a port, name=number (repeatable)")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "show passwords")
	return cmd
}

func portsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ports <name>",
		Short: "Show which ports to forward and open for an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			r, err := a.PortReport(args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), r)
			return nil
		},
	}
}

func consoleCmd() *cobra.Command {
	var info, showSecrets, stdin bool
	cmd := &cobra.Command{
		Use:   "console <name> [command...]",
		Short: "Use a running server's console: run one command, or open a live session",
		Long: `With a command, runs it and prints the server's reply. Without one, opens a
live console until you press Ctrl-D (the server keeps running).

Servers with RCON are reached through it, so replies come back directly.
Others are typed into the server's own console, and you see its output.
Stacks open their console program (such as a database shell) in a container.

--info shows how to reach the console with other tools, such as an RCON
client on another computer.`,
		Example: `  anyserve console friends
  anyserve console my-rust serverinfo
  anyserve console my-cs2 --info --show-secrets`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			name := args[0]
			if info {
				return printConsoleInfo(a, name, showSecrets, out)
			}
			opts := app.ConsoleOptions{Stdin: stdin}
			if len(args) > 1 {
				reply, err := a.ConsoleCommand(name, strings.Join(args[1:], " "), opts)
				if reply != "" {
					fmt.Fprintln(out, strings.TrimRight(reply, "\n"))
				}
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			tty := isatty.IsTerminal(os.Stdin.Fd())
			return a.ConsoleSession(ctx, name, opts, cmd.InOrStdin(), out, tty)
		},
	}
	cmd.Flags().BoolVar(&info, "info", false, "show how to connect with other tools")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "with --info, show the RCON password")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "use the server's own console even if it has RCON")
	return cmd
}

func printConsoleInfo(a *app.App, name string, showSecrets bool, out io.Writer) error {
	ci, err := a.ConsoleDetails(name)
	if err != nil {
		return err
	}
	switch ci.Kind {
	case app.ConsoleRCON:
		pw := "(hidden; add --show-secrets)"
		if showSecrets {
			pw = ci.Password
		}
		fmt.Fprintf(out, "RCON protocol: %s\nPort:          %d/tcp\nPassword:      %s\n\n", ci.Protocol, ci.Port, pw)
		fmt.Fprintf(out, "From this machine:  anyserve console %s\n", name)
		fmt.Fprintf(out, "From elsewhere:     anyserve rcon <this-host>:%d --protocol %s\n", ci.Port, ci.Protocol)
		if ci.Protocol == "webrcon" {
			fmt.Fprintf(out, "Other tools:        any Rust WebRCON client (e.g. RustAdmin) at ws://<this-host>:%d/<password>\n", ci.Port)
		} else {
			fmt.Fprintf(out, "Other tools:        any Source RCON client (e.g. rcon-cli, mcrcon) at <this-host>:%d\n", ci.Port)
		}
		if !ci.Public {
			fmt.Fprintf(out, "\nThe RCON port is meant to stay private. To reach it from another computer, use\nan SSH tunnel (ssh -L %d:127.0.0.1:%d <this-host>) rather than opening it to the internet.\n", ci.Port, ci.Port)
		}
	case app.ConsoleExec:
		what := ci.Description
		if what == "" {
			what = "a console program"
		}
		fmt.Fprintf(out, "Console: %s, run in the %q container: %s\n", what, ci.Service, strings.Join(ci.Command, " "))
		fmt.Fprintf(out, "Open it with: anyserve console %s\nOne command:  anyserve console %s '<command>'\n", name, name)
	default:
		fmt.Fprintf(out, "This server has no RCON, so its console is only reachable on this machine,\nthrough Anyserve: anyserve console %s\n", name)
		fmt.Fprintln(out, "Others can use it by signing in to this machine (for example over SSH) as the user that runs the server.")
	}
	return nil
}

func rconCmd() *cobra.Command {
	var protocol, passwordEnv string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "rcon <host:port> [command...]",
		Short: "Talk to any server's remote console, on this machine or another",
		Long: `Connects to a game server's RCON. The password is read from the environment
variable named by --password-env (ANYSERVE_RCON_PASSWORD by default), or asked
for, so it doesn't end up in your shell history.

Protocols: source (Counter-Strike, Minecraft, ARK and many more) and webrcon (Rust).`,
		Example: `  anyserve rcon game.example.com:28016 --protocol webrcon
  ANYSERVE_RCON_PASSWORD=... anyserve rcon 10.0.0.5:27015 status`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !rcon.Known(protocol) {
				return fmt.Errorf("--protocol must be one of %s", strings.Join(rcon.Protocols, ", "))
			}
			if _, _, err := net.SplitHostPort(args[0]); err != nil {
				return fmt.Errorf("%q: use host:port", args[0])
			}
			pw := os.Getenv(passwordEnv)
			if pw == "" {
				if !isatty.IsTerminal(os.Stdin.Fd()) {
					return fmt.Errorf("set %s to the RCON password", passwordEnv)
				}
				if err := huh.NewInput().Title("RCON password").EchoMode(huh.EchoModePassword).Value(&pw).Run(); err != nil {
					return cancelled(err)
				}
			}
			c, err := rcon.Dial(rcon.Options{Protocol: protocol, Addr: args[0], Password: pw, Timeout: timeout})
			if err != nil {
				return err
			}
			defer c.Close()
			out := cmd.OutOrStdout()
			if len(args) > 1 {
				reply, err := c.Exec(strings.Join(args[1:], " "))
				if reply != "" {
					fmt.Fprintln(out, strings.TrimRight(reply, "\n"))
				}
				return err
			}
			fmt.Fprintf(out, "Connected to %s. Type commands; press Ctrl-D to leave.\n", args[0])
			sc := bufio.NewScanner(cmd.InOrStdin())
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				reply, err := c.Exec(line)
				if reply != "" {
					fmt.Fprintln(out, strings.TrimRight(reply, "\n"))
				}
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&protocol, "protocol", "source", "source or webrcon")
	cmd.Flags().StringVar(&passwordEnv, "password-env", "ANYSERVE_RCON_PASSWORD", "environment variable holding the password")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "how long to wait for the server")
	return cmd
}

func sayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "say <name> <message...>",
		Short: "Broadcast a chat message to players",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			return a.Say(args[0], strings.Join(args[1:], " "))
		},
	}
}

func backupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup <name>",
		Short: "Back up an instance's world and settings now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			_, err = a.MakeBackup(args[0])
			return err
		},
	}
}

func backupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backups <name>",
		Short: "List an instance's backups",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			list, err := a.Backups(args[0])
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No backups yet.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CREATED\tSIZE\tFILE")
			for _, b := range list {
				fmt.Fprintf(w, "%s\t%.1f MB\t%s\n", b.Created.Local().Format(time.DateTime), float64(b.Size)/(1<<20), b.Path)
			}
			return w.Flush()
		},
	}
}

func restoreCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <name> <backup-file>",
		Short: "Restore a backup over a stopped instance (a safety backup is taken first)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if !yes && interactive() {
				ok, err := confirm(fmt.Sprintf("Replace %s's world files with %s?", args[0], args[1]))
				if err != nil || !ok {
					return errors.New("cancelled")
				}
			}
			return a.Restore(args[0], args[1])
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func removeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Stop and delete an instance (backups are kept)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if !yes {
				if !interactive() {
					return errors.New("removing deletes the server and its world; pass --yes to confirm")
				}
				ok, err := confirm(fmt.Sprintf("Delete %s, including its world? Backups are kept.", args[0]))
				if err != nil || !ok {
					return errors.New("cancelled")
				}
			}
			if err := a.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func refreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Download the latest service definitions from every registry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			return a.Refresh(ctx)
		},
	}
}

func registryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "registry", Short: "Manage service registries"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured registries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			srcs, err := a.Sources()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tURL\tKEY")
			for _, s := range srcs {
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.URL, s.PublicKey)
			}
			return w.Flush()
		},
	}, &cobra.Command{
		Use:     "add <name> <index-url> <public-key>",
		Short:   "Add a registry; its index must be signed by the given key",
		Example: `  anyserve registry add mine https://example.com/registry/index.json anyserve-ed25519:AbC...`,
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			if err := a.AddRegistry(ctx, registry.Source{Name: args[0], URL: args[1], PublicKey: args[2]}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added registry %s.\n", args[0])
			return nil
		},
	}, &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			return a.RemoveRegistry(args[0])
		},
	})
	return cmd
}

func steamCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "steam", Short: "Sign in to Steam for servers that can't download anonymously"}
	cmd.AddCommand(&cobra.Command{
		Use:   "login <steam-user>",
		Short: "Sign in through SteamCMD so it caches your session",
		Long: `Runs SteamCMD's own sign-in. SteamCMD asks for your password and any
Steam Guard code and keeps the session itself; Anyserve saves only the user
name. Use an account that owns the game (a separate account is safer).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			if err := a.SteamLogin(ctx, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed in as %s. Servers that need a Steam account will use it.\n", args[0])
			return nil
		},
	}, &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved Steam user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.SteamLogout(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Forgot the Steam user. SteamCMD's cached session stays in its own folder until it expires.")
			return nil
		},
	})
	return cmd
}

func targetsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "targets",
		Short: "List the places Anyserve can run services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TARGET\tAVAILABLE\tDESCRIPTION")
			for _, t := range target.All() {
				avail := "yes"
				if err := t.Available(); err != nil {
					avail = "no: " + err.Error()
				}
				if t.Name() == target.Default() {
					avail += " (default)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name(), avail, t.Description())
			}
			return w.Flush()
		},
	}
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run <name>",
		Short:  "Run an instance in the foreground (service managers use this)",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			return a.Run(context.Background(), args[0])
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the Anyserve version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "anyserve %s (%s/%s)\n", version.Current(), runtime.GOOS, runtime.GOARCH)
		},
	}
}

func printWarnings(cmd *cobra.Command, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", w)
	}
}

// folderMounts describes where a folder setting is mounted, for info.
func folderMounts(d *spec.Definition, key string) []string {
	var out []string
	if d.Stack == nil {
		return []string{"not mounted"}
	}
	for _, sv := range d.Stack.Services {
		for _, v := range sv.Volumes {
			if v.Setting != key {
				continue
			}
			mode := "read-write"
			if v.ReadOnly {
				mode = "read-only"
			}
			out = append(out, fmt.Sprintf("%s sees it at %s, %s", sv.Name, v.Target, mode))
		}
	}
	if len(out) == 0 {
		return []string{"not mounted"}
	}
	return out
}
