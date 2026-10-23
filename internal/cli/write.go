package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/evadcmd/ghzt/internal/workspace"
)

// maxBodyChars is the longest body GitHub takes for an issue, pull request or
// comment. Longer ones are refused before any request, with the length.
const maxBodyChars = 65536

// bodyFlags are -b/--body and -F/--body-file, which take a body as text or
// from a file, "-" for standard input, as in gh.
type bodyFlags struct {
	body, file string
}

func addBodyFlags(cmd *cobra.Command, usage string) *bodyFlags {
	f := &bodyFlags{}
	cmd.Flags().StringVarP(&f.body, "body", "b", "", usage)
	cmd.Flags().StringVarP(&f.file, "body-file", "F", "", "Read body text from file (use \"-\" to read from standard input)")
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
	return f
}

// given reports whether either flag was set, an empty body included.
func (f *bodyFlags) given(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file")
}

// read returns the body. The file is read inside the workspace root, so a
// body can't carry a file from anywhere else to GitHub.
func (f *bodyFlags) read(s IO, cmd *cobra.Command) (string, error) {
	body := f.body
	if cmd.Flags().Changed("body-file") {
		root, err := workspace.FromEnv(s.getenv)
		if err != nil {
			return "", err
		}
		defer func() { _ = root.Close() }()
		data, err := root.ReadInput(f.file, s.In, utf8.UTFMax*maxBodyChars)
		if err != nil {
			return "", err
		}
		body = string(data)
	}
	if err := checkBodyLen("body", body); err != nil {
		return "", err
	}
	return body, nil
}

// checkBodyLen returns an error if text, a body named what, is longer than
// GitHub takes.
func checkBodyLen(what, text string) error {
	if n := utf8.RuneCountInString(text); n > maxBodyChars {
		return fmt.Errorf("%s is too long: %d characters, the maximum is %d", what, n, maxBodyChars)
	}
	return nil
}

// readRequired is read for commands that need a body, such as a comment.
func (f *bodyFlags) readRequired(s IO, cmd *cobra.Command) (string, error) {
	if !f.given(cmd) {
		return "", errors.New("--body or --body-file required when not running interactively")
	}
	body, err := f.read(s, cmd)
	if err == nil && body == "" {
		err = errors.New("body cannot be blank")
	}
	return body, err
}

// setEdits are --add-<name> and --remove-<name> for a set of labels or
// assignees. GitHub replaces the whole set on an update, so the edit is
// applied to the current one and the result sent back.
type setEdits struct {
	add, remove []string
}

func addSetEditFlags(cmd *cobra.Command, name, what string) *setEdits {
	e := &setEdits{}
	cmd.Flags().StringSliceVar(&e.add, "add-"+name, nil, "Add "+what+" by name")
	cmd.Flags().StringSliceVar(&e.remove, "remove-"+name, nil, "Remove "+what+" by name")
	return e
}

func (e *setEdits) changed() bool { return len(e.add) > 0 || len(e.remove) > 0 }

// apply returns current with the removals dropped and the additions appended,
// in order and without duplicates. GitHub matches label names and logins
// case-insensitively, so this does too, and keeps the spelling already on the
// issue.
func (e *setEdits) apply(current []string) []string {
	out := []string{}
	for _, v := range append(slices.Clone(current), e.add...) {
		if !containsFold(e.remove, v) && !containsFold(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, v) })
}

// newCommentCmd is gh issue comment and gh pr comment: both are a comment on
// the issue's conversation.
func newCommentCmd(s IO, kind string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comment <number>",
		Short: "Add a comment to " + kind,
		Long: "Add a comment to " + kind + ".\n\n" +
			"Prints the URL of the new comment.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "The comment body text")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		text, err := body.readRequired(s, c)
		if err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		var comment apiComment
		path := repo.ref.path("issues", strconv.Itoa(number), "comments")
		if err := api.send(ctx, http.MethodPost, path, record{"body": text}, &comment); err != nil {
			return err
		}
		return writeURL(s.Out, comment.URL)
	}
	return cmd
}

// writeURL prints the URL of what a command created or changed, which is what
// gh prints when stdout isn't a terminal.
func writeURL(w io.Writer, u string) error {
	_, err := fmt.Fprintln(w, u)
	return err
}
