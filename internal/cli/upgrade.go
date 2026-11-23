package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/selfupdate"
	"github.com/jonsjava/anyserve/internal/target"
	"github.com/jonsjava/anyserve/internal/version"
)

func upgradeCmd() *cobra.Command {
	var check bool
	var from, ver string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update Anyserve itself to the latest release",
		Long: `upgrade downloads the newest Anyserve release for this machine, checks it
against the checksums signed with Anyserve's release key, and replaces this
binary. The API service restarts on the new version if it's running. Your
servers keep running throughout.`,
		Example: `  anyserve upgrade --check
  anyserve upgrade`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ctx, cancel := signalContext()
			defer cancel()
			var r selfupdate.Release
			if from != "" {
				// For testing and mirrors: a folder (file://...) or URL
				// holding a release's files.
				if ver == "" {
					return fmt.Errorf("--from needs --version")
				}
				r = selfupdate.Release{Version: ver, Base: strings.TrimSuffix(from, "/") + "/"}
			} else {
				var err error
				if r, err = selfupdate.Latest(ctx); err != nil {
					return err
				}
			}
			cur := version.Current()
			if !selfupdate.Newer(r.Version) {
				fmt.Fprintf(out, "Anyserve %s is up to date (latest release %s).\n", cur, r.Version)
				return nil
			}
			if check {
				fmt.Fprintf(out, "Anyserve %s is available (this is %s). Run `anyserve upgrade` to install it.\n", r.Version, cur)
				return nil
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			fmt.Fprintf(out, "Downloading Anyserve %s...\n", r.Version)
			bin, err := selfupdate.Fetch(ctx, r, runtime.GOOS, runtime.GOARCH, selfupdate.ReleasePublicKey)
			if err != nil {
				return err
			}
			if err := selfupdate.Replace(exe, bin); err != nil {
				return err
			}
			fmt.Fprintf(out, "Upgraded %s from %s to %s (signature and checksum verified).\n", exe, cur, r.Version)
			if runtime.GOOS == "linux" && target.ServeState() == "active" {
				if err := target.RestartServe(); err != nil {
					fmt.Fprintf(out, "Restart the API service yourself: %v\n", err)
				} else {
					fmt.Fprintln(out, "Restarted the API service on the new version.")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only say whether a newer release exists")
	cmd.Flags().StringVar(&from, "from", "", "install from this folder or URL instead of GitHub (needs --version)")
	cmd.Flags().StringVar(&ver, "version", "", "the version found at --from")
	cmd.Flags().MarkHidden("from")
	cmd.Flags().MarkHidden("version")
	return cmd
}
