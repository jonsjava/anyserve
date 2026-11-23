package cli

import "github.com/spf13/cobra"

func proxyCmd() *cobra.Command { return &cobra.Command{Use: "proxy", Hidden: true} }
