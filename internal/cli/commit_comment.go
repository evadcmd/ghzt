package cli

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/spf13/cobra"
)

// shaRE is a full or abbreviated commit SHA, which is what the commit
// comments API takes; a branch name isn't.
var shaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// newCommitCommentCmd is ghzt commit-comment. gh has no command for it: it
// comments on a commit, where gh api would otherwise be needed.
func newCommitCommentCmd(s IO) *cobra.Command {
	var (
		path     string
		position int
	)
	cmd := &cobra.Command{
		Use:   "commit-comment <sha>",
		Short: "Add a comment to a commit",
		Long: "Add a comment to a commit, or with --path to a file in its diff.\n\n" +
			"--position is the line index in the file's diff, not its line number,\n" +
			"as the API takes it. GitHub doesn't check --path against the diff: a\n" +
			"file that isn't in it still gets the comment. Prints the URL of the\n" +
			"new comment.",
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	body := addBodyFlags(cmd, "The comment body text")
	cmd.Flags().StringVar(&path, "path", "", "File in the commit's diff to comment on")
	cmd.Flags().IntVar(&position, "position", 0, "Line index in the file's diff to comment on (requires --path)")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		sha := args[0]
		if !shaRE.MatchString(sha) {
			return fmt.Errorf("invalid commit SHA %q: expected 7 to 40 lowercase hex digits", sha)
		}
		fields := record{}
		if c.Flags().Changed("path") {
			if err := checkRepoPath(path); err != nil {
				return err
			}
			fields["path"] = path
		}
		if c.Flags().Changed("position") {
			if path == "" {
				return errors.New("--position requires --path")
			}
			if position < 1 {
				return fmt.Errorf("invalid position %d: must be at least 1", position)
			}
			fields["position"] = position
		}
		text, err := body.readRequired(s, c)
		if err != nil {
			return err
		}
		fields["body"] = text
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		var comment apiComment
		if err := api.send(ctx, http.MethodPost, repo.ref.path("commits", sha, "comments"), fields, &comment); err != nil {
			return err
		}
		return writeURL(s.Out, comment.URL)
	}
	return cmd
}
