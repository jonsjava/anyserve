// Command anyserve hosts game servers and other services on any platform.
package main

import (
	"os"

	"github.com/jonsjava/anyserve/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
