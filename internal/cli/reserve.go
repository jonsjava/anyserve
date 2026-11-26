package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/jonsjava/anyserve/internal/app"
	"github.com/spf13/cobra"
)

func reserveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reserve",
		Short: "Keep ports free for things Anyserve doesn't run",
		Long: `Reserved ports are never picked for new servers. Reserve ports that
something else owns but isn't always listening on, e.g. another panel's
stopped servers or a range your host's firewall keeps for something else.
Anyserve already avoids ports its own servers use and ports in use right
now; a reservation also covers the ports that are quiet at the moment.`,
		Example: `  anyserve reserve add 27015-27030 --note "Pterodactyl"
  anyserve reserve add 25565 --protocol tcp
  anyserve reserve list
  anyserve reserve remove 27015-27030`,
	}
	var proto, note string
	add := &cobra.Command{
		Use:   "add <port|from-to>",
		Short: "Reserve a port or a range",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			from, to, err := app.ParsePortRange(args[0])
			if err != nil {
				return err
			}
			r := app.PortReservation{From: from, To: to, Protocol: proto, Note: note}
			inUse, err := a.Reserve(r)
			if err != nil {
				return err
			}
			if r.Protocol == "" {
				r.Protocol = "both"
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Reserved %s.\n", r)
			if len(inUse) > 0 {
				fmt.Fprintf(out, "Already used by your servers, which keep them: %s\n", strings.Join(inUse, ", "))
			}
			return nil
		},
	}
	add.Flags().StringVar(&proto, "protocol", "both", "tcp, udp or both")
	add.Flags().StringVar(&note, "note", "", "what the ports are for")
	list := &cobra.Command{
		Use:   "list",
		Short: "List reserved ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			rs, err := a.Reservations()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(rs) == 0 {
				fmt.Fprintln(out, "No ports are reserved. Add some with `anyserve reserve add`.")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PORTS\tPROTOCOL\tNOTE")
			for _, r := range rs {
				ports := fmt.Sprint(r.From)
				if r.To != r.From {
					ports += "-" + fmt.Sprint(r.To)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", ports, r.Protocol, r.Note)
			}
			return w.Flush()
		},
	}
	remove := &cobra.Command{
		Use:     "remove <port|from-to>",
		Aliases: []string{"rm"},
		Short:   "Remove a reservation (give it exactly as reserved)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			from, to, err := app.ParsePortRange(args[0])
			if err != nil {
				return err
			}
			n, err := a.Unreserve(from, to)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %d reservation(s) of %s.\n", n, args[0])
			return nil
		},
	}
	cmd.AddCommand(add, list, remove)
	return cmd
}
