package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/remote"
	"github.com/jonsjava/anyserve/internal/spec"
)

// apiServer builds the API server with an audit log in the data folder.
func apiServer(a *app.App) (*api.Server, io.Closer, error) {
	if err := os.MkdirAll(a.Dirs.Data, 0o700); err != nil {
		return nil, nil, err
	}
	audit, err := os.OpenFile(a.Dirs.AuditLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	s := &api.Server{NewApp: app.New, Audit: audit, IssueCert: issueWebCert}
	return s, audit, nil
}

// issueWebCert issues a 90-day browser client certificate from this
// host's web CA.
func issueWebCert(a *app.App, user string) (*api.IssuedCert, error) {
	host, _ := os.Hostname()
	ca, err := remote.LoadOrCreateWebCA(a.Dirs.Config, host)
	if err != nil {
		return nil, err
	}
	cc, err := ca.IssueClientCert(user, 90*24*time.Hour, spec.RandomPassword(16))
	if err != nil {
		return nil, err
	}
	return &api.IssuedCert{Serial: cc.Serial, NotAfter: cc.NotAfter.Format(time.DateOnly), P12: cc.P12, Password: cc.Password}, nil
}

func serveCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the API on a local socket (and the SSH server, if remote access is on)",
		Long: `serve runs Anyserve's API for the web UI and remote users. The API listens
on a unix socket only you can open; it never listens on the network itself.
When remote access is on (` + "`anyserve remote enable`" + `), serve also runs the
built-in SSH server, which is how remote users reach the API.

` + "`anyserve remote enable`" + ` installs serve as a user service, so you rarely need
to run it yourself.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if socket == "" {
				socket = a.Dirs.Socket()
			}
			s, audit, err := apiServer(a)
			if err != nil {
				return err
			}
			defer audit.Close()
			ctx, cancel := signalContext()
			defer cancel()
			l, err := api.ListenUnix(socket)
			if err != nil {
				return err
			}
			defer os.Remove(socket)
			fmt.Fprintf(cmd.OutOrStdout(), "API listening on %s\n", socket)
			errs := make(chan error, 2)
			go func() { errs <- s.ServeLocal(ctx, l) }()
			if err := startRemote(ctx, cmd, a, s, errs); err != nil {
				return err
			}
			if err := startAgent(ctx, cmd, a, s); err != nil {
				return err
			}
			err = <-errs
			cancel()
			return err
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "unix socket path (default: anyserve.sock in the data folder)")
	return cmd
}
