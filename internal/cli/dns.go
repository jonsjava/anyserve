package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func dnsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dns <name>",
		Short: "Show the DNS records to create for an instance's domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			adv, err := a.DNS(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Create these records at your DNS provider:")
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "  NAME\tTYPE\tVALUE")
			for _, r := range adv.Records {
				fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Name, r.Type, r.Value)
			}
			w.Flush()
			if len(adv.Connect) > 0 {
				fmt.Fprintln(out, "\nThen connect with:")
				for _, c := range adv.Connect {
					fmt.Fprintf(out, "  %s\n", c)
				}
			}
			for _, n := range adv.Notes {
				fmt.Fprintf(out, "\n%s\n", n)
			}
			return nil
		},
	}
}
