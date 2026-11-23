package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/app"
)

func autoUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auto-update",
		Short: "Check for and apply updates on a schedule",
		Long: `Scheduled updates check each instance for a changed definition in the
registry and, for Steam servers, a newer Steam build. What happens next
depends on the instance's policy:

  off     not checked
  notify  recorded, shown in ` + "`anyserve list`" + ` and sent to the notify URL (default)
  apply   backed up, updated, then watched; if the server isn't healthy
          within the health limit, the update is rolled back and held

Turn on the schedule with ` + "`anyserve auto-update enable`" + `.`,
		Example: `  anyserve auto-update set friends apply
  anyserve auto-update enable --at "*-*-* 04:00"
  anyserve auto-update run --dry-run
  anyserve auto-update notify https://ntfy.sh/my-private-topic`,
	}
	cmd.AddCommand(autoUpdateSetCmd(), autoUpdateRunCmd(), autoUpdateStatusCmd(),
		autoUpdateEnableCmd(), autoUpdateDisableCmd(), autoUpdateNotifyCmd())
	return cmd
}

func autoUpdateSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "set <name> <off|notify|apply>",
		Short:     "Choose what scheduled updates do for an instance",
		Args:      cobra.ExactArgs(2),
		ValidArgs: app.Policies,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.SetPolicy(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scheduled updates for %s: %s.\n", args[0], args[1])
			if a.Schedule() == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "The schedule isn't on yet: `anyserve auto-update enable`.")
			}
			return nil
		},
	}
}

func autoUpdateRunCmd() *cobra.Command {
	var opts app.AutoUpdateOptions
	cmd := &cobra.Command{
		Use:   "run [name...]",
		Short: "Check now, and apply updates to instances whose policy is apply",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signalContext()
			defer cancel()
			opts.Names = args
			results, err := a.AutoUpdate(ctx, opts)
			printUpdateResults(cmd, results)
			if err != nil {
				return err
			}
			for _, r := range results {
				if r.Outcome == "failed" || r.Outcome == "rolled back" {
					return fmt.Errorf("%s: %s", r.Name, r.Outcome)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "only report what would be updated")
	cmd.Flags().DurationVar(&opts.Warn, "warn", 0, "warn players this long before stopping, e.g. 5m")
	cmd.Flags().DurationVar(&opts.Health, "health-limit", 10*time.Minute, "how long an updated server has to become healthy")
	cmd.Flags().DurationVar(&opts.Settle, "settle", time.Minute, "how long it must then stay healthy")
	cmd.Flags().BoolVar(&opts.NoRefresh, "no-refresh", false, "use the cached registry index")
	return cmd
}

func printUpdateResults(cmd *cobra.Command, results []app.UpdateResult) {
	if len(results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No instances to check.")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPOLICY\tOUTCOME\tUPDATE\tDETAIL")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Policy, r.Outcome, r.Available, r.Detail)
	}
	w.Flush()
}

func autoUpdateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the schedule, each instance's policy and the last results",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if s := a.Schedule(); s != "" {
				fmt.Fprintf(out, "Schedule: %s\n", s)
			} else {
				fmt.Fprintln(out, "Schedule: off (`anyserve auto-update enable`)")
			}
			if c, err := a.LoadConfig(); err == nil && c.NotifyURL != "" {
				fmt.Fprintf(out, "Notify:   %s\n", c.NotifyURL)
			}
			list, _, err := a.Store.List()
			if err != nil {
				return err
			}
			state, _ := a.UpdateState()
			fmt.Fprintln(out)
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPOLICY\tLAST CHECK\tOUTCOME\tDETAIL")
			for _, in := range list {
				r, ok := state[in.Name]
				checked, outcome := "never", ""
				detail := ""
				if ok {
					checked = r.Checked.Local().Format(time.DateTime)
					outcome = r.Outcome
					detail = strings.Trim(strings.Join([]string{r.Available, r.Detail}, "; "), "; ")
				}
				if in.HeldDefinition != "" || in.HeldBuild != "" {
					detail = strings.Trim("held after a failed update; "+detail, "; ")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", in.Name, app.Policy(in), checked, outcome, detail)
			}
			return w.Flush()
		},
	}
}

func autoUpdateEnableCmd() *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Run scheduled updates automatically (a systemd user timer)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.EnableSchedule(at); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scheduled updates are on: %s.\nInstances default to notify; choose apply per instance with `anyserve auto-update set <name> apply`.\n", a.Schedule())
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "*-*-* 04:00", "when to run, as a systemd OnCalendar time (a random delay of up to 30 minutes is added)")
	return cmd
}

func autoUpdateDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Stop running scheduled updates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.DisableSchedule(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Scheduled updates are off.")
			return nil
		},
	}
}

func autoUpdateNotifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "notify <url|off>",
		Short: "Send a summary to a URL (an ntfy topic, or anything that takes a plain-text POST)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			c, err := a.LoadConfig()
			if err != nil {
				return err
			}
			u := args[0]
			if u == "off" {
				u = ""
			} else if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
				return fmt.Errorf("give an http(s) URL, or off")
			}
			c.NotifyURL = u
			if err := a.SaveConfig(c); err != nil {
				return err
			}
			if u == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Update notifications are off.")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Update summaries will be posted to %s.\n", u)
			}
			return nil
		},
	}
}
