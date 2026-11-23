package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/jonsjava/anyserve/internal/api"
	"github.com/jonsjava/anyserve/internal/app"
)

// startRemote starts the SSH server when remote access is enabled.
// (Filled in by the remote-access step.)
func startRemote(ctx context.Context, cmd *cobra.Command, a *app.App, s *api.Server, errs chan<- error) error {
	return nil
}
