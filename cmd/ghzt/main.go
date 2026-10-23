// Command ghzt is a cut-down gh and git that talk to the GitHub API instead of
// a local checkout. Install it once and symlink it as gh and git: it picks the
// command tree by the name it is run as.
package main

import (
	"os"

	"github.com/evadcmd/ghzt/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, cli.IO{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}))
}
