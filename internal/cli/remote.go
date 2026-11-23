package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/remote"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/target"
)

// startRemote starts the SSH server in `serve` when remote access is on.
func startRemote(ctx context.Context, cmd *cobra.Command, a *app.App, s *api.Server, errs chan<- error) error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Remote.Enabled {
		return nil
	}
	listen := cfg.Remote.Listen
	if listen == "" {
		listen = app.DefaultRemoteListen
	}
	key, _, err := remote.LoadOrCreateHostKey(a.Dirs.Config)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("remote access: %w", err)
	}
	host, _ := os.Hostname()
	ca, err := remote.LoadOrCreateWebCA(a.Dirs.Config, host)
	if err != nil {
		return fmt.Errorf("web CA: %w", err)
	}
	srv := &remote.Server{
		WebCA:   ca,
		App:     func() (*app.App, error) { return app.New(os.Stderr) },
		API:     s.Handler(),
		HostKey: key,
		Audit:   s.Audit,
		Log:     func(f string, args ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", args...) },
	}
	fmt.Fprintf(cmd.OutOrStdout(), "SSH server listening on %s (host key %s)\n", l.Addr(), remote.Fingerprint(key.PublicKey()))
	go func() { errs <- srv.Serve(ctx, l) }()
	return nil
}

func usersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Manage who may manage instances remotely",
		Long: `Users sign in to the built-in SSH server with their SSH keys and see and manage
only the instances granted to them. Admins manage every instance, but only
in the web UI with a browser client certificate issued on this host
(` + "`anyserve users cert`" + `); otherwise an admin is an ordinary user. Managing users
is done here, on the host, never remotely.`,
		Example: `  anyserve users add alice --key ~/alice.pub
  anyserve users grant alice friends-rust
  anyserve users add sam --key "ssh-ed25519 AAAA... sam@laptop" --admin`,
	}
	var keys []string
	var admin bool
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a user with one or more public keys",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				return errors.New("give at least one public key with --key (a .pub file or the key itself)")
			}
			ks, err := readKeys(keys)
			if err != nil {
				return err
			}
			if err := a.AddUser(args[0], admin, ks); err != nil {
				return err
			}
			role := "a user"
			if admin {
				role = "an admin"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s as %s.\n", args[0], role)
			if !admin {
				fmt.Fprintf(cmd.OutOrStdout(), "Grant instances with `anyserve users grant %s <instance>`.\n", args[0])
			}
			return nil
		},
	}
	add.Flags().StringArrayVar(&keys, "key", nil, "public key, or a file holding them (repeatable)")
	add.Flags().BoolVar(&admin, "admin", false, "may manage every instance")

	var addKeys []string
	addKey := &cobra.Command{
		Use:   "add-key <name>",
		Short: "Add public keys to a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ks, err := readKeys(addKeys)
			if err != nil {
				return err
			}
			return a.UpdateUser(args[0], func(u *app.User) error {
				u.Keys = append(u.Keys, ks...)
				return nil
			})
		},
	}
	addKey.Flags().StringArrayVar(&addKeys, "key", nil, "public key, or a file holding them (repeatable)")

	var certOut string
	var certValid time.Duration
	cert := &cobra.Command{
		Use:   "cert <name>",
		Short: "Issue a browser client certificate to an admin (on the host only)",
		Long: `Admins use their admin powers in the web UI only when their browser presents a
client certificate issued here, on the host. Without one, an admin's web UI
shows only the instances granted to them, like any user's.

This writes a .p12 file and prints its import password once. The admin
imports it into their browser (Settings → Privacy and security → Manage
certificates → Your certificates → Import) and, once, trusts the host's web
CA (shown by ` + "`anyserve proxy`" + `), which only covers localhost addresses.`,
		Example: `  anyserve users cert sam --out sam.p12
  anyserve users cert-revoke sam --all`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			cfg, err := a.LoadConfig()
			if err != nil {
				return err
			}
			u, err := cfg.FindUser(args[0])
			if err != nil {
				return err
			}
			if !u.Admin {
				return fmt.Errorf("%s isn't an admin, and a certificate only matters for admins (`anyserve users admin %s true` first)", u.Name, u.Name)
			}
			if certOut == "" {
				certOut = u.Name + ".p12"
			}
			host, _ := os.Hostname()
			ca, err := remote.LoadOrCreateWebCA(a.Dirs.Config, host)
			if err != nil {
				return err
			}
			pw := spec.RandomPassword(16)
			cc, err := ca.IssueClientCert(u.Name, certValid, pw)
			if err != nil {
				return err
			}
			if err := os.WriteFile(certOut, cc.P12, 0o600); err != nil {
				return err
			}
			if err := a.UpdateUser(u.Name, func(u *app.User) error {
				u.CertSerials = append(u.CertSerials, cc.Serial)
				return nil
			}); err != nil {
				os.Remove(certOut)
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Wrote %s: a browser client certificate for %s, valid until %s (serial %s).\n", certOut, u.Name, cc.NotAfter.Format(time.DateOnly), cc.Serial)
			fmt.Fprintf(out, "Import password (shown only now): %s\n", cc.Password)
			fmt.Fprintln(out, "Send the file and the password to the admin by different routes, then delete your copy of the file.")
			return nil
		},
	}
	cert.Flags().StringVar(&certOut, "out", "", "where to write the .p12 file (default <name>.p12)")
	cert.Flags().DurationVar(&certValid, "valid", 90*24*time.Hour, "how long the certificate is valid")

	var revokeAll bool
	revoke := &cobra.Command{
		Use:   "cert-revoke <name> [serial...]",
		Short: "Revoke browser client certificates",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if !revokeAll && len(args) < 2 {
				return errors.New("give serial numbers (`anyserve users list` shows how many there are) or --all")
			}
			return a.UpdateUser(args[0], func(u *app.User) error {
				if revokeAll {
					u.CertSerials = nil
					return nil
				}
				var keep []string
				for _, s := range u.CertSerials {
					drop := false
					for _, r := range args[1:] {
						drop = drop || strings.EqualFold(s, r)
					}
					if !drop {
						keep = append(keep, s)
					}
				}
				u.CertSerials = keep
				return nil
			})
		},
	}
	revoke.Flags().BoolVar(&revokeAll, "all", false, "revoke all of this user's certificates")

	cmd.AddCommand(add, addKey, cert, revoke,
		&cobra.Command{
			Use:   "list",
			Short: "List users, their roles and instances",
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
				if len(cfg.Users) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No users yet: `anyserve users add <name> --key <file>`.")
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tROLE\tKEYS\tCERTS\tINSTANCES")
				for _, u := range cfg.Users {
					role, inst := "user", strings.Join(u.Instances, ", ")
					if u.Admin {
						role, inst = "admin", "all"
					}
					fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", u.Name, role, len(u.Keys), len(u.CertSerials), inst)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "remove <name>",
			Short: "Remove a user",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := newApp(cmd)
				if err != nil {
					return err
				}
				if err := a.RemoveUser(args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Removed %s. Connections they have open lose access to instances at once and can't reconnect.\n", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use:   "grant <name> <instance...>",
			Short: "Let a user manage instances",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := newApp(cmd)
				if err != nil {
					return err
				}
				return a.Grant(args[0], args[1:]...)
			},
		},
		&cobra.Command{
			Use:   "revoke <name> <instance...>",
			Short: "Take instances away from a user",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := newApp(cmd)
				if err != nil {
					return err
				}
				return a.Revoke(args[0], args[1:]...)
			},
		},
		&cobra.Command{
			Use:       "admin <name> <true|false>",
			Short:     "Make a user an admin, or not",
			Args:      cobra.ExactArgs(2),
			ValidArgs: []string{"true", "false"},
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := newApp(cmd)
				if err != nil {
					return err
				}
				v := args[1] == "true"
				if !v && args[1] != "false" {
					return errors.New("say true or false")
				}
				return a.UpdateUser(args[0], func(u *app.User) error { u.Admin = v; return nil })
			},
		},
	)
	return cmd
}

// readKeys reads keys given inline or as files, one key per line.
func readKeys(args []string) ([]string, error) {
	var out []string
	for _, k := range args {
		lines := []string{k}
		if b, err := os.ReadFile(k); err == nil {
			lines = strings.Split(string(b), "\n")
		} else if !strings.Contains(k, " ") {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if strings.Contains(l, "PRIVATE KEY") {
				return nil, errors.New("that's a private key; give the .pub file")
			}
			n, err := app.NormalizeKey(l)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no keys found")
	}
	return out, nil
}

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Remote access: this host's SSH server, and other hosts you manage",
		Long: `On a host: ` + "`remote enable`" + ` starts Anyserve's own SSH server (keys only, no shell)
so users added with ` + "`anyserve users`" + ` can manage their instances from their own
computers. It's off until you enable it, and listens on this machine only
unless you give --listen.

On your computer: ` + "`remote add`" + ` remembers a host, then ` + "`anyserve proxy <host>`" + `
opens the web UI and ` + "`anyserve port-forward`" + ` reaches an instance's port, both
on your own 127.0.0.1. SFTP clients connect to the same address.`,
		Example: `  # on the host
  anyserve users add alice --key alice.pub && anyserve users grant alice friends
  anyserve remote enable --listen 0.0.0.0:7022

  # on alice's computer
  anyserve remote add box alice@box.example.com:7022 --key ~/.ssh/id_ed25519
  anyserve proxy box
  sftp -P 7022 alice@box.example.com`,
	}
	var listen string
	enable := &cobra.Command{
		Use:   "enable",
		Short: "Start this host's SSH server for remote users",
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
			if listen != "" {
				if _, _, err := net.SplitHostPort(listen); err != nil {
					return fmt.Errorf("--listen: give host:port, e.g. 0.0.0.0:7022 (%v)", err)
				}
				cfg.Remote.Listen = listen
			}
			if cfg.Remote.Listen == "" {
				cfg.Remote.Listen = app.DefaultRemoteListen
			}
			cfg.Remote.Enabled = true
			key, created, err := remote.LoadOrCreateHostKey(a.Dirs.Config)
			if err != nil {
				return err
			}
			hostname, _ := os.Hostname()
			if _, err := remote.LoadOrCreateWebCA(a.Dirs.Config, hostname); err != nil {
				return err
			}
			if err := a.SaveConfig(cfg); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if runtime.GOOS == "linux" {
				if err := target.InstallServe(a.TargetEnv()); err != nil {
					return fmt.Errorf("installing the serve service: %w", err)
				}
				fmt.Fprintln(out, "Remote access is on (user service anyserve-serve).")
			} else {
				fmt.Fprintln(out, "Remote access is on. Run `anyserve serve` (as a service) to accept connections.")
			}
			if created {
				fmt.Fprintln(out, "Created a host key.")
			}
			fmt.Fprintf(out, "Listening on: %s\nHost key:     %s\n", cfg.Remote.Listen, remote.Fingerprint(key.PublicKey()))
			fmt.Fprintln(out, "Users check that fingerprint when they first connect.")
			if h, _, _ := net.SplitHostPort(cfg.Remote.Listen); h == "127.0.0.1" || h == "localhost" || h == "::1" {
				fmt.Fprintln(out, "It listens on this machine only; use --listen 0.0.0.0:7022 to accept other computers (and open that TCP port in your firewall).")
			}
			if len(cfg.Users) == 0 {
				fmt.Fprintln(out, "No users yet: `anyserve users add <name> --key <file.pub>`.")
			}
			return nil
		},
	}
	enable.Flags().StringVar(&listen, "listen", "", "address and port to listen on (default "+app.DefaultRemoteListen+")")

	var keyFile, fingerprint string
	var yes bool
	add := &cobra.Command{
		Use:   "add <name> <user@host[:port]>",
		Short: "Remember another host to manage from this computer",
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
			if _, err := cfg.FindHost(args[0]); err == nil {
				return fmt.Errorf("there's already a host called %q; remove it first", args[0])
			}
			user, addr, ok := strings.Cut(args[1], "@")
			if !ok || user == "" || addr == "" {
				return errors.New("give the address as user@host or user@host:port")
			}
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addr = net.JoinHostPort(addr, "7022")
			}
			h := remote.Host{Name: args[0], Addr: addr, User: user, Key: keyFile}
			key, err := remote.Probe(h)
			if key == nil {
				return fmt.Errorf("couldn't reach %s: %w", addr, err)
			}
			fp := remote.Fingerprint(key)
			out := cmd.OutOrStdout()
			switch {
			case fingerprint != "":
				if fingerprint != fp {
					return fmt.Errorf("the host's key is %s, not %s; not trusting it", fp, fingerprint)
				}
			case yes:
			case interactive():
				fmt.Fprintf(out, "%s's host key is %s\nCheck it matches what `anyserve remote status` shows on the host. Trust it? [y/N] ", addr, fp)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
					return errors.New("not added")
				}
			default:
				return fmt.Errorf("the host's key is %s; pass --fingerprint %s to trust it", fp, fp)
			}
			if err != nil {
				return fmt.Errorf("the host answered, but signing in failed (is %s added with this key on the host?): %w", user, err)
			}
			h.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			cfg.Hosts = append(cfg.Hosts, app.RemoteHost(h))
			if err := a.SaveConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(out, "Added %s. Open its web UI with `anyserve proxy %s`.\n", args[0], args[0])
			return nil
		},
	}
	add.Flags().StringVar(&keyFile, "key", "", "private key file (default: use ssh-agent)")
	add.Flags().StringVar(&fingerprint, "fingerprint", "", "the host key fingerprint you expect (SHA256:...)")
	add.Flags().BoolVarP(&yes, "yes", "y", false, "trust the host's key without asking")

	cmd.AddCommand(enable,
		&cobra.Command{
			Use:   "disable",
			Short: "Stop this host's SSH server",
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
				cfg.Remote.Enabled = false
				if err := a.SaveConfig(cfg); err != nil {
					return err
				}
				if runtime.GOOS == "linux" {
					if err := target.RemoveServe(); err != nil {
						return err
					}
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Remote access is off. Users and the host key are kept.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show this host's remote access settings",
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
				state := "off"
				if cfg.Remote.Enabled {
					state = "on"
					if s := target.ServeState(); s != "" {
						state += " (service " + s + ")"
					}
				}
				listen := cfg.Remote.Listen
				if listen == "" {
					listen = app.DefaultRemoteListen
				}
				fmt.Fprintf(out, "Remote access: %s\nListen:        %s\n", state, listen)
				if b, err := os.ReadFile(remote.HostKeyPath(a.Dirs.Config)); err == nil {
					if s, err := ssh.ParsePrivateKey(b); err == nil {
						fmt.Fprintf(out, "Host key:      %s\n", remote.Fingerprint(s.PublicKey()))
					}
				}
				fmt.Fprintf(out, "Users:         %d\nAudit log:     %s\n", len(cfg.Users), a.Dirs.AuditLog())
				if len(cfg.Hosts) > 0 {
					fmt.Fprintf(out, "Hosts you manage: %d (`anyserve remote list`)\n", len(cfg.Hosts))
				}
				return nil
			},
		},
		add,
		&cobra.Command{
			Use:   "list",
			Short: "List hosts you manage from this computer",
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
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tADDRESS\tUSER\tHOST KEY")
				for _, h := range cfg.Hosts {
					fp := "?"
					if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.HostKey)); err == nil {
						fp = remote.Fingerprint(k)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", h.Name, h.Addr, h.User, fp)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "remove <name>",
			Short: "Forget a host",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := newApp(cmd)
				if err != nil {
					return err
				}
				cfg, err := a.LoadConfig()
				if err != nil {
					return err
				}
				if _, err := cfg.FindHost(args[0]); err != nil {
					return err
				}
				var keep []app.RemoteHost
				for _, h := range cfg.Hosts {
					if h.Name != args[0] {
						keep = append(keep, h)
					}
				}
				cfg.Hosts = keep
				return a.SaveConfig(cfg)
			},
		},
	)
	return cmd
}

// connect dials a saved host.
func connect(a *app.App, name string) (*ssh.Client, error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return nil, err
	}
	h, err := cfg.FindHost(name)
	if err != nil {
		return nil, err
	}
	return remote.Dial(remote.Host(*h))
}

func portForwardCmd() *cobra.Command {
	var local string
	cmd := &cobra.Command{
		Use:   "port-forward <host> <instance> <port-name>",
		Short: "Reach a remote instance's TCP port on this computer",
		Example: `  anyserve port-forward box friends rcon           # then connect to the printed 127.0.0.1 port
  anyserve port-forward box vault http --local 127.0.0.1:8080`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			c, err := connect(a, args[0])
			if err != nil {
				return err
			}
			defer c.Close()
			resp, err := remote.APIClient(c).Get(remote.APIURL + "/v1/instances/" + args[1])
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				var e api.Error
				json.NewDecoder(resp.Body).Decode(&e)
				return fmt.Errorf("%s: %s", args[1], e.Error)
			}
			var in api.Instance
			if err := json.NewDecoder(resp.Body).Decode(&in); err != nil {
				return err
			}
			port := 0
			var names []string
			for _, p := range in.Ports {
				names = append(names, p.Name)
				if p.Name == args[2] {
					if p.Protocol == "udp" {
						return fmt.Errorf("%s is a UDP port; SSH forwarding carries TCP only", p.Name)
					}
					port = p.Port
				}
			}
			if port == 0 {
				return fmt.Errorf("%s has no port %q (it has: %s)", args[1], args[2], strings.Join(names, ", "))
			}
			if local == "" {
				local = "127.0.0.1:0"
			}
			l, err := net.Listen("tcp", local)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Forwarding %s → %s %s (port %d on %s). Ctrl-C to stop.\n", l.Addr(), args[1], args[2], port, args[0])
			ctx, cancel := signalContext()
			defer cancel()
			return remote.Forward(ctx, c, l, port, func(f string, args ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", args...) })
		},
	}
	cmd.Flags().StringVar(&local, "local", "", "local address to listen on (default: a free 127.0.0.1 port)")
	return cmd
}
