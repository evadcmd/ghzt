package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/evadcmd/ghzt/internal/workspace"
)

const (
	// maxCommitFiles is how many files one git commit takes. Each is a
	// request of its own.
	maxCommitFiles = 100
	// maxMessageBytes is the longest commit message -F reads.
	maxMessageBytes = 1 << 20
	// maxBlobBytes is the largest file git commit uploads: GitHub refuses a
	// file over 100 MiB.
	maxBlobBytes = 100 << 20
)

// cleanMessage tidies a commit message the way git commit does by default
// for -m and -F: trailing whitespace is stripped from each line, runs of
// blank lines become one, and leading and trailing blank lines are dropped.
// The result ends with a newline, or is empty.
func cleanMessage(msg string) string {
	var lines []string
	blank := false
	for line := range strings.SplitSeq(msg, "\n") {
		line = strings.TrimRight(line, " \t\r\v\f")
		if line == "" {
			blank = len(lines) > 0
			continue
		}
		if blank {
			lines = append(lines, "")
			blank = false
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// commitPaths checks the paths to commit, which are both repo paths and paths
// under GHZT_ROOT, and drops repeats.
func commitPaths(args []string) ([]string, error) {
	var paths []string
	for _, p := range args {
		if err := checkRepoPath(p); err != nil {
			return nil, err
		}
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	if len(paths) > maxCommitFiles {
		return nil, fmt.Errorf("%d files to commit, the maximum is %d", len(paths), maxCommitFiles)
	}
	return paths, nil
}

type (
	apiGitRef struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}

	apiGitCommit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}

	apiSHA struct {
		SHA string `json:"sha"`
	}
)

func newGitCommitCmd(s IO) *cobra.Command {
	var (
		messages     []string
		file, branch string
	)
	cmd := &cobra.Command{
		Use:   "commit --branch <branch> (-m <msg> | -F <file>) [--] <path>...",
		Short: "Commit files to a branch",
		Long: `Commit files under GHZT_ROOT to a branch on GitHub, as one commit on
top of the branch's head. Each <path> is both the file under GHZT_ROOT and
its path in the repo; a symlink is refused. A file is committed executable
(100755) if its owner can execute it, and as 100644 otherwise.

Unlike git, there is no index or checkout: --branch names the branch to
commit to, which must exist (create it with git branch). Files are added or
changed, never deleted, and the commit is made even if nothing changed. The
author is whoever the token belongs to.

The branch is moved without force, so if it moved on during the commit, the
update fails and the branch is left as it was.

Like git, several -m are joined as paragraphs, and the message is cleaned
up: trailing whitespace and extra blank lines are dropped.

Prints "[<branch> <short sha>] <subject>", the number of files, and the
commit's URL.`,
		Args: cobra.MinimumNArgs(1),
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().StringVar(&branch, "branch", "", "The branch to commit to")
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "Use the given `msg` as the commit message; several are joined as paragraphs")
	cmd.Flags().StringVarP(&file, "file", "F", "", "Take the commit message from `file` under GHZT_ROOT (use \"-\" to read from standard input)")
	_ = cmd.MarkFlagRequired("branch")
	cmd.MarkFlagsMutuallyExclusive("message", "file")
	cmd.MarkFlagsOneRequired("message", "file")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if err := checkNewBranch(branch); err != nil {
			return err
		}
		paths, err := commitPaths(args)
		if err != nil {
			return err
		}
		root, err := workspace.FromEnv(s.getenv)
		if err != nil {
			return err
		}
		defer func() { _ = root.Close() }()
		msg := strings.Join(messages, "\n\n")
		if c.Flags().Changed("file") {
			data, err := root.ReadInput(file, s.In, maxMessageBytes)
			if err != nil {
				return err
			}
			msg = string(data)
		}
		if msg = cleanMessage(msg); msg == "" {
			return errors.New("aborting commit due to empty commit message")
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		sha, err := commitFiles(ctx, api, repo.ref, branch, root, paths, msg)
		if err != nil {
			return err
		}
		// Like git's %s: the first paragraph, its lines joined by spaces.
		para, _, _ := strings.Cut(strings.TrimSuffix(msg, "\n"), "\n\n")
		subject := strings.ReplaceAll(para, "\n", " ")
		_, err = fmt.Fprintf(s.Out, "[%s %s] %s\n %d %s changed\n", branch, sha[:7], subject, len(paths), plural(len(paths), "file"))
		if err != nil {
			return err
		}
		return writeURL(s.Out, "https://github.com/"+repo.ref.String()+"/commit/"+sha)
	}
	return cmd
}

// commitFiles commits paths, read from root, to branch with msg through the
// git database API, and returns the new commit's SHA. Files are read and
// uploaded one at a time, so only one is in memory, held about three times
// over (raw, base64 and the request body); if one fails, the blobs already
// uploaded are left unreferenced, which changes nothing in the repo.
//
// Every SHA a response returns is checked before it goes into the next
// request: an empty base_tree would make a tree of only these files, and the
// commit would delete everything else.
func commitFiles(ctx context.Context, api *apiClient, repo repoRef, branch string, root *workspace.Root, paths []string, msg string) (string, error) {
	var ref apiGitRef
	if _, err := api.get(ctx, repo.path(withSegments(branch, "git", "ref", "heads")...), nil, &ref); err != nil {
		// apiError's format: a missing branch is the likely 404 here.
		if strings.HasPrefix(err.Error(), "HTTP 404:") {
			return "", fmt.Errorf("%w; if branch %s doesn't exist, create it with git branch", err, branch)
		}
		return "", err
	}
	parent := ref.Object.SHA
	if ref.Object.Type != "commit" || !fullSHARE.MatchString(parent) {
		return "", fmt.Errorf("unexpected response resolving branch %s: %s %q", branch, ref.Object.Type, parent)
	}
	var head apiGitCommit
	if _, err := api.get(ctx, repo.path("git", "commits", parent), nil, &head); err != nil {
		return "", err
	}
	if !fullSHARE.MatchString(head.Tree.SHA) {
		return "", fmt.Errorf("unexpected response reading commit %s: tree %q", parent, head.Tree.SHA)
	}
	entries := make([]record, len(paths))
	for i, p := range paths {
		data, mode, err := root.ReadFileMode(p, maxBlobBytes)
		if err != nil {
			return "", err
		}
		var blob apiSHA
		body := record{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"}
		if err := api.send(ctx, http.MethodPost, repo.path("git", "blobs"), body, &blob); err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		if !fullSHARE.MatchString(blob.SHA) {
			return "", fmt.Errorf("%s: unexpected response creating the blob: %q", p, blob.SHA)
		}
		fileMode := "100644"
		if mode&0o100 != 0 {
			fileMode = "100755"
		}
		entries[i] = record{"path": p, "mode": fileMode, "type": "blob", "sha": blob.SHA}
	}
	var tree apiSHA
	if err := api.send(ctx, http.MethodPost, repo.path("git", "trees"), record{"base_tree": head.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if !fullSHARE.MatchString(tree.SHA) {
		return "", fmt.Errorf("unexpected response creating the tree: %q", tree.SHA)
	}
	var commit apiSHA
	body := record{"message": msg, "tree": tree.SHA, "parents": []string{parent}}
	if err := api.send(ctx, http.MethodPost, repo.path("git", "commits"), body, &commit); err != nil {
		return "", err
	}
	if !fullSHARE.MatchString(commit.SHA) {
		return "", fmt.Errorf("unexpected response creating the commit: %q", commit.SHA)
	}
	// Without force, the update fails if the branch is no longer at parent.
	body = record{"sha": commit.SHA, "force": false}
	if err := api.send(ctx, http.MethodPatch, repo.path(withSegments(branch, "git", "refs", "heads")...), body, nil); err != nil {
		return "", fmt.Errorf("%w; commit %s was made but %s was not moved to it", err, commit.SHA, branch)
	}
	return commit.SHA, nil
}
