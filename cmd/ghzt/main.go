// Command ghzt is a cut-down gh and git that talk to the GitHub API instead of
// a local checkout. Install it once and symlink it as gh and git: it picks the
// command tree by the name it is run as.
package main

import (
	"context"
	"os"

	"github.com/evadcmd/ghzt/internal/cli"
	"github.com/evadcmd/ghzt/internal/telemetry"
)

func main() {
	os.Exit(run())
}

// run is main without the os.Exit, so that the deferred flush runs first.
func run() int {
	log, shutdown := telemetry.Setup(context.Background())
	defer shutdown()
	return cli.Run(os.Args, cli.IO{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr, Log: log})
}
