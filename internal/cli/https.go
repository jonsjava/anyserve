package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/https"
)

func httpsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "https",
		Short: "Serve apps at their own domains over HTTPS",
		Long: `Give an app a domain (` + "`anyserve config vault --domain vault.example.com`" + `) and
Anyserve puts it behind an HTTPS reverse proxy with a free Let's Encrypt
certificate. The app itself keeps listening on 127.0.0.1 only.

The proxy is one of:

  caddy    the default: Anyserve runs Caddy for you (ports 80 and 443 must
           reach this machine, and each domain's DNS must point at it)
  nginx    you run nginx; Anyserve writes a config file for it to include
  traefik  you run Traefik; Anyserve writes a file-provider config for it
  none     no proxy; domains can't be set`,
		Example: `  anyserve config vault --domain vault.example.com
  anyserve https use caddy --email you@example.com
  anyserve https use nginx
  anyserve https status`,
	}
	var email string
	use := &cobra.Command{
		Use:       "use <caddy|nginx|traefik|none>",
		Short:     "Choose the HTTPS proxy",
		Args:      cobra.ExactArgs(1),
		ValidArgs: https.Providers,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.SetHTTPSProvider(args[0], email, cmd.Flags().Changed("email")); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch args[0] {
			case https.Caddy:
				fmt.Fprintln(out, "Anyserve's Caddy is the HTTPS proxy. It runs while any app has a domain.")
			case https.None:
				fmt.Fprintln(out, "No HTTPS proxy.")
			default:
				fmt.Fprintf(out, "Using your own %s. Anyserve keeps %s up to date; include it in %s's config.\n", args[0], a.HTTPSFile(args[0]), args[0])
			}
			return nil
		},
	}
	use.Flags().StringVar(&email, "email", "", "email for Let's Encrypt expiry notices (Caddy)")
	cmd.AddCommand(use,
		&cobra.Command{
			Use:   "status",
			Short: "Show the proxy and which apps have domains",
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
				p := cfg.HTTPSProvider()
				state := ""
				switch p {
				case https.Caddy:
					state = " (stopped)"
					if a.HTTPSRunning() {
						state = " (running)"
					}
				case https.Nginx, https.Traefik:
					state = " (config file: " + a.HTTPSFile(p) + ")"
				}
				fmt.Fprintf(out, "Proxy: %s%s\n", p, state)
				if cfg.HTTPS.Email != "" {
					fmt.Fprintf(out, "Email: %s\n", cfg.HTTPS.Email)
				}
				sites, err := a.Sites()
				if err != nil {
					return err
				}
				if len(sites) == 0 {
					fmt.Fprintln(out, "No apps have domains yet (`anyserve config <name> --domain <domain>`).")
					return nil
				}
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "\nDOMAIN\tINSTANCE\tLOCAL PORT")
				for _, s := range sites {
					fmt.Fprintf(w, "https://%s\t%s\t127.0.0.1:%d\n", s.Domain, s.Instance, s.Port)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "sync",
			Short: "Rewrite the proxy's config from the apps' domains",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := newApp(cmd)
				if err != nil {
					return err
				}
				return a.SyncHTTPS()
			},
		},
	)
	return cmd
}
