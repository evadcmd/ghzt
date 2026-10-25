// Package cli builds the command trees. One binary serves as gh, git and
// ghzt, picked by the name it is invoked as (argv[0]), the way busybox does;
// under any other name it is ghzt, which has gh and git as subcommands.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	// Log gets one record per run, for ghzt's own telemetry; nil drops it.
	// It must not write to Out or ErrOut, which only carry what gh and git
	// would print.
	Log *slog.Logger
	// Getenv reads the environment the credentials come from; os.Getenv if
	// nil.
	Getenv func(string) string
	// Transport sends the API requests; http.DefaultTransport if nil. Tests
	// point it at an httptest server.
	Transport http.RoundTripper
}

func (s IO) getenv(key string) string {
	if s.Getenv == nil {
		return os.Getenv(key)
	}
	return s.Getenv(key)
}

// Run executes the command line args (args[0] included) and returns the exit
// code. Errors go to ErrOut, prefixed with the name the binary was run as.
//
// The log record names the command and the flags that were set, but carries
// no positional args or flag values: those can be whole issue bodies, commit
// messages or an Authorization header passed to gh api.
func Run(args []string, s IO) int {
	log := s.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	name := filepath.Base(args[0])
	cmd := newCommand(name, s)
	cmd.SetArgs(args[1:])
	start := time.Now()
	ran, err := cmd.ExecuteC()
	attrs := []any{
		"name", name,
		"command", ran.CommandPath(),
		"flags", setFlags(ran),
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if err != nil {
		_, _ = fmt.Fprintf(s.ErrOut, "%s: %v\n", name, err)
		if fe, ok := errors.AsType[*flagError](err); ok {
			_, _ = fmt.Fprintf(s.ErrOut, "Run '%s --help' for usage.\n", fe.cmdPath)
		}
		log.Error("run", append(attrs, "exit_code", exitError, "error", err)...)
		return exitError
	}
	log.Info("run", append(attrs, "exit_code", exitOK)...)
	return exitOK
}

// setFlags returns the names of the flags set on the command line, sorted.
func setFlags(cmd *cobra.Command) []string {
	names := []string{}
	cmd.Flags().Visit(func(f *pflag.Flag) { names = append(names, f.Name) })
	return names
}

func newCommand(name string, s IO) *cobra.Command {
	switch name {
	case "gh":
		return newGhCmd(s)
	case "git":
		return newGitCmd(s)
	default:
		cmd := newRoot("ghzt", "gh and git backed by the GitHub API", s)
		cmd.AddCommand(newGhCmd(s), newGitCmd(s), newCommitCommentCmd(s),
			newReviewReplyCmd(s), newReviewResolveCmd(s))
		return cmd
	}
}

func newGhCmd(s IO) *cobra.Command {
	cmd := newRoot("gh", "Work with GitHub issues, pull requests and repos", s)
	cmd.AddCommand(newIssueCmd(s), newPRCmd(s), newLabelCmd(s), newSearchCmd(s))
	return cmd
}

func newGitCmd(s IO) *cobra.Command {
	cmd := newRoot("git", "Read and write a GitHub repo's git data", s)
	cmd.AddCommand(newGitShowCmd(s), newGitLsTreeCmd(s), newGitBranchCmd(s), newGitCommitCmd(s), newGitMergeCmd(s))
	return cmd
}

// newRoot returns a top-level command with the settings every tree shares.
// Usage isn't printed on errors: the caller is an agent, and a full help page
// per mistake costs more context than the one-line hint does.
//
// The command is runnable, printing its help, so that cobra checks its args:
// a command without Run returns help with a nil error before validating
// them, and an unknown subcommand would then exit 0.
func newRoot(use, short string, s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:           use,
		Short:         short,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(c *cobra.Command, _ []string) error { return c.Help() },
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
