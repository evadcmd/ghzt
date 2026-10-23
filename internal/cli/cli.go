// Package cli builds the command trees. One binary serves as gh, git and
// ghzt, picked by the name it is invoked as (argv[0]), the way busybox does;
// under any other name it is ghzt, which has gh and git as subcommands.
package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X ...cli.version=...".
var version = "dev"

const (
	exitOK    = 0
	exitError = 1
)

// IO is where commands read input and write output, so tests can capture it.
type IO struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// Run executes the command line args (args[0] included) and returns the exit
// code. Errors go to ErrOut, prefixed with the name the binary was run as.
func Run(args []string, s IO) int {
	name := filepath.Base(args[0])
	cmd := newCommand(name, s)
	cmd.SetArgs(args[1:])
	if err := cmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(s.ErrOut, "%s: %v\n", name, err)
		if fe, ok := errors.AsType[*flagError](err); ok {
			_, _ = fmt.Fprintf(s.ErrOut, "Run '%s --help' for usage.\n", fe.cmdPath)
		}
		return exitError
	}
	return exitOK
}

func newCommand(name string, s IO) *cobra.Command {
	switch name {
	case "gh":
		return newGhCmd(s)
	case "git":
		return newGitCmd(s)
	default:
		cmd := newRoot("ghzt", "gh and git backed by the GitHub API", s)
		cmd.AddCommand(newGhCmd(s), newGitCmd(s))
		return cmd
	}
}

func newGhCmd(s IO) *cobra.Command {
	return newRoot("gh", "Work with GitHub issues, pull requests and repos", s)
}

func newGitCmd(s IO) *cobra.Command {
	return newRoot("git", "Read and write a GitHub repo's git data", s)
}

// newRoot returns a top-level command with the settings every tree shares.
// Usage isn't printed on errors: the caller is an agent, and a full help page
// per mistake costs more context than the one-line hint does.
func newRoot(use, short string, s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:           use,
		Short:         short,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.SetIn(s.In)
	cmd.SetOut(s.Out)
	cmd.SetErr(s.ErrOut)
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &flagError{err: err, cmdPath: c.CommandPath()}
	})
	return cmd
}

// flagError is a flag parsing error, which Run follows with a pointer to the
// command's help.
type flagError struct {
	err     error
	cmdPath string
}

func (e *flagError) Error() string { return e.err.Error() }
func (e *flagError) Unwrap() error { return e.err }
