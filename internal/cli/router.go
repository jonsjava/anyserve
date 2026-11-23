package cli

import (
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/https"
	"github.com/jonsjava/anyserve/internal/spec"
)

func routerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "router",
		Short: "Share the Minecraft port between servers by domain",
		Long: `Give Minecraft servers domains and they all share the standard port:
players type play.example.com or creative.example.com with no port, and
Anyserve's router passes each player to the right server. The router runs
while any Minecraft server has a domain; each server keeps a port of its
own on this machine, which players never see.`,
		Example: `  anyserve config survival --domain play.example.com
  anyserve router
  anyserve router port 25570`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			routes, err := a.Routes()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			state := "stopped"
			if a.RouterRunning() {
				state = "running"
			}
			c, err := a.LoadConfig()
			if err != nil {
				return err
			}
			port := c.Router.Port
			if port == 0 {
				port = https.DefaultRouterPort
			}
			fmt.Fprintf(out, "Router: %s on port %d\n", state, port)
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			n := 0
			for _, r := range routes {
				if r.Kind != spec.RouteMinecraft {
					continue
				}
				if n == 0 {
					fmt.Fprintln(w, "\nDOMAIN\tINSTANCE\tPORT")
				}
				n++
				fmt.Fprintf(w, "%s\t%s\t%d\n", r.Domain, r.Instance, r.Port)
			}
			w.Flush()
			if n == 0 {
				fmt.Fprintln(out, "No Minecraft server has a domain (`anyserve config <name> --domain play.example.com`).")
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "port <port>",
		Short: "Change the port players connect to (default 25565)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("%q isn't a port number", args[0])
			}
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.SetRouterPort(n); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "The Minecraft router uses port %d.\n", n)
			return nil
		},
	})
	return cmd
}
