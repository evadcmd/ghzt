package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// fullSHARE is the 40-digit SHA the API resolves a ref to.
var fullSHARE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// branchURL is the branch's page, which the API doesn't return.
func branchURL(repo repoRef, branch string) string {
	segs := strings.Split(branch, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return "https://github.com/" + repo.String() + "/tree/" + strings.Join(segs, "/")
}

// checkNewBranch checks the name of a branch to create: a ref, as checkRef
// takes it, that git would also accept (git check-ref-format).
func checkNewBranch(name string) error {
	if err := checkRef(name); err != nil || name == "HEAD" || name == "@" || strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return fmt.Errorf("invalid branch name %q", name)
		}
	}
	return nil
}

func newGitBranchCmd(s IO) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch <branchname> [<start-point>]",
		Short: "Create a branch",
		Long: `Create a branch on GitHub at <start-point>: a branch, tag or SHA, HEAD
(the default branch) if omitted. There are no remotes, so a start point is
"main", not "origin/main".

Prints the URL of the new branch. Listing, renaming and deleting branches
are not supported.`,
		Args: cobra.RangeArgs(1, 2),
	}
	repo := addRepoFlag(cmd)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		name, start := args[0], "HEAD"
		if len(args) == 2 {
			start = args[1]
		}
		if err := checkNewBranch(name); err != nil {
			return err
		}
		if err := checkRef(start); err != nil {
			return err
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		// The sha media type returns just the commit's SHA, not the commit
		// with its files.
		data, _, err := api.getRaw(ctx, repo.ref.path(withSegments(start, "commits")...), nil, "application/vnd.github.sha", 100)
		if err != nil {
			return err
		}
		sha := strings.TrimSpace(string(data))
		if !fullSHARE.MatchString(sha) {
			return fmt.Errorf("unexpected response resolving %s: %q", start, sha)
		}
		body := record{"ref": "refs/heads/" + name, "sha": sha}
		if err := api.send(ctx, http.MethodPost, repo.ref.path("git", "refs"), body, nil); err != nil {
			return err
		}
		return writeURL(s.Out, branchURL(repo.ref, name))
	}
	return cmd
}
