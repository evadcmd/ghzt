package cli

import (
	"context"
	"crypto/sha1" //nolint:gosec // G505: git's object IDs are SHA-1.
	"encoding/base64"
	"encoding/hex"
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
	// maxBlobBytes is the largest file git commit uploads: GitHub blocks a
	// file over 100 MiB in a push, and the API is assumed to do the same.
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
// under GHZT_ROOT, and the paths to remove, and drops repeats.
func commitPaths(args, rm []string) (paths, removed []string, err error) {
	dedupe := func(in []string) ([]string, error) {
		var out []string
		for _, p := range in {
			if err := checkRepoPath(p); err != nil {
				return nil, err
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
		return out, nil
	}
	if paths, err = dedupe(args); err != nil {
		return nil, nil, err
	}
	if removed, err = dedupe(rm); err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 && len(removed) == 0 {
		return nil, nil, errors.New("nothing to commit: give a <path> or --rm")
	}
	for _, p := range removed {
		if slices.Contains(paths, p) {
			return nil, nil, fmt.Errorf("%s is both committed and removed", p)
		}
	}
	if n := len(paths) + len(removed); n > maxCommitFiles {
		return nil, nil, fmt.Errorf("%d files to commit, the maximum is %d", n, maxCommitFiles)
	}
	return paths, removed, nil
}

// blobSHA is the SHA git gives a blob of data.
func blobSHA(data []byte) string {
	h := sha1.New() //nolint:gosec // G401: git's object ID, not a security hash.
	// A hash's Write never fails.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
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

	treeEntry struct {
		Mode, Type, SHA string
	}
)

// baseTree looks paths up in a commit's tree, one directory at a time, each
// read once: a recursive listing can be truncated, and then a path missing
// from it might still be there.
type baseTree struct {
	api  *apiClient
	repo repoRef
	root string
	dirs map[string]map[string]treeEntry // by tree SHA
}

// errNotDir is what lookup returns when a directory in a path is a file or a
// submodule: nothing can be put under it.
var errNotDir = errors.New("is not a directory")

// lookup returns the entry at p, and false if there is none. If a directory
// in p is a file or a submodule instead, the error wraps errNotDir.
func (t *baseTree) lookup(ctx context.Context, p string) (treeEntry, bool, error) {
	segs := strings.Split(p, "/")
	sha := t.root
	for i, name := range segs[:len(segs)-1] {
		e, ok, err := t.entry(ctx, sha, name)
		if err != nil || !ok {
			return treeEntry{}, false, err
		}
		if e.Type != "tree" {
			return treeEntry{}, false, fmt.Errorf("%s %w", strings.Join(segs[:i+1], "/"), errNotDir)
		}
		sha = e.SHA
	}
	return t.entry(ctx, sha, segs[len(segs)-1])
}

func (t *baseTree) entry(ctx context.Context, sha, name string) (treeEntry, bool, error) {
	entries, ok := t.dirs[sha]
	if !ok {
		if !fullSHARE.MatchString(sha) {
			return treeEntry{}, false, fmt.Errorf("unexpected response reading a tree: SHA %q", sha)
		}
		var tree apiTree
		if _, err := t.api.get(ctx, t.repo.path("git", "trees", sha), nil, &tree); err != nil {
			return treeEntry{}, false, err
		}
		if tree.Truncated {
			return treeEntry{}, false, fmt.Errorf("tree %s is too large to read", sha)
		}
		entries = make(map[string]treeEntry, len(tree.Tree))
		for _, e := range tree.Tree {
			entries[e.Path] = treeEntry{Mode: e.Mode, Type: e.Type, SHA: e.SHA}
		}
		t.dirs[sha] = entries
	}
	e, ok := entries[name]
	return e, ok, nil
}

func newGitCommitCmd(s IO) *cobra.Command {
	var (
		messages, rm []string
		file, branch string
	)
	cmd := &cobra.Command{
		Use:   "commit --branch <branch> (-m <msg> | -F <file>) [--rm <path>]... [--] [<path>...]",
		Short: "Commit files to a branch",
		Long: `Commit files under GHZT_ROOT to a branch on GitHub, as one commit on
top of the branch's head. Each <path> is both the file under GHZT_ROOT and
its path in the repo; a symlink is refused. A file is committed executable
(100755) if its owner can execute it, and as 100644 otherwise.

Unlike git, there is no index or checkout: --branch names the branch to
commit to, which must exist (create it with git branch). A file missing
under GHZT_ROOT is an error, not a deletion: --rm deletes a file from the
branch, and must name one that is there. Files the same as on the branch
are left out, and if nothing is left, there is no commit. The author is
whoever the token belongs to.

The branch is moved without force, so if it moved on during the commit, the
update fails and the branch is left as it was. If the update gets no answer,
or a server error, the branch is read back to tell whether it was moved.

Like git, several -m are joined as paragraphs, and the message is cleaned
up: trailing whitespace and extra blank lines are dropped.

Prints "[<branch> <short sha>] <subject>", the number of files changed and
deleted, and the commit's URL.`,
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().StringVar(&branch, "branch", "", "The branch to commit to")
	cmd.Flags().StringArrayVar(&rm, "rm", nil, "Delete the file at `path` from the branch")
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "Use the given `msg` as the commit message; several are joined as paragraphs")
	cmd.Flags().StringVarP(&file, "file", "F", "", "Take the commit message from `file` under GHZT_ROOT (use \"-\" to read from standard input)")
	_ = cmd.MarkFlagRequired("branch")
	cmd.MarkFlagsMutuallyExclusive("message", "file")
	cmd.MarkFlagsOneRequired("message", "file")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if err := checkNewBranch(branch); err != nil {
			return err
		}
		paths, removed, err := commitPaths(args, rm)
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
		sha, n, err := commitFiles(ctx, api, repo.ref, branch, root, paths, removed, msg)
		if err != nil {
			return err
		}
		// Like git's %s: the first paragraph, its lines joined by spaces.
		para, _, _ := strings.Cut(strings.TrimSuffix(msg, "\n"), "\n\n")
		subject := strings.ReplaceAll(para, "\n", " ")
		_, err = fmt.Fprintf(s.Out, "[%s %s] %s\n %d %s changed\n", branch, sha[:7], subject, n, plural(n, "file"))
		if err != nil {
			return err
		}
		return writeURL(s.Out, "https://github.com/"+repo.ref.String()+"/commit/"+sha)
	}
	return cmd
}

// commitFiles commits paths, read from root, and the deletion of removed to
// branch with msg through the git database API, and returns the new commit's
// SHA and the number of files it changes. Files are read and uploaded one at
// a time, so only one is in memory, held about three times over (raw, base64
// and the request body); if one fails, the blobs already uploaded are left
// unreferenced, which changes nothing in the repo.
//
// Every SHA a response returns is checked before it goes into the next
// request: an empty base_tree would make a tree of only these files, and the
// commit would delete everything else.
func commitFiles(ctx context.Context, api *apiClient, repo repoRef, branch string, root *workspace.Root, paths, removed []string, msg string) (string, int, error) {
	var ref apiGitRef
	if _, err := api.get(ctx, repo.path(withSegments(branch, "git", "ref", "heads")...), nil, &ref); err != nil {
		// apiError's format: a missing branch is the likely 404 here.
		if strings.HasPrefix(err.Error(), "HTTP 404:") {
			return "", 0, fmt.Errorf("%w; if branch %s doesn't exist, create it with git branch", err, branch)
		}
		return "", 0, err
	}
	parent := ref.Object.SHA
	if ref.Object.Type != "commit" || !fullSHARE.MatchString(parent) {
		return "", 0, fmt.Errorf("unexpected response resolving branch %s: %s %q", branch, ref.Object.Type, parent)
	}
	var head apiGitCommit
	if _, err := api.get(ctx, repo.path("git", "commits", parent), nil, &head); err != nil {
		return "", 0, err
	}
	if !fullSHARE.MatchString(head.Tree.SHA) {
		return "", 0, fmt.Errorf("unexpected response reading commit %s: tree %q", parent, head.Tree.SHA)
	}
	base := &baseTree{api: api, repo: repo, root: head.Tree.SHA, dirs: map[string]map[string]treeEntry{}}

	// Deletions are checked first, so a wrong --rm uploads nothing.
	var entries []record
	for _, p := range removed {
		e, ok, err := base.lookup(ctx, p)
		switch {
		case err != nil && !errors.Is(err, errNotDir):
			return "", 0, err
		case !ok:
			return "", 0, fmt.Errorf("--rm %s: no such file on branch %s", p, branch)
		case e.Type == "tree":
			return "", 0, fmt.Errorf("--rm %s: a directory on branch %s; give its files", p, branch)
		}
		// A null SHA deletes the path; mode and type must still match it.
		entries = append(entries, record{"path": p, "mode": e.Mode, "type": e.Type, "sha": nil})
	}
	for _, p := range paths {
		data, mode, err := root.ReadFileMode(p, maxBlobBytes)
		if err != nil {
			return "", 0, err
		}
		fileMode := "100644"
		if mode&0o100 != 0 {
			fileMode = "100755"
		}
		sha := blobSHA(data)
		e, ok, err := base.lookup(ctx, p)
		if errors.Is(err, errNotDir) {
			return "", 0, fmt.Errorf("%s: %w on branch %s", p, err, branch)
		}
		if err != nil {
			return "", 0, err
		}
		if ok && e.Type == "tree" {
			return "", 0, fmt.Errorf("%s: a directory on branch %s", p, branch)
		}
		if ok && e.SHA == sha {
			if e.Mode == fileMode {
				continue
			}
			// Only the mode changed: the blob is there already.
			entries = append(entries, record{"path": p, "mode": fileMode, "type": "blob", "sha": sha})
			continue
		}
		var blob apiSHA
		body := record{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"}
		if err := api.send(ctx, http.MethodPost, repo.path("git", "blobs"), body, &blob); err != nil {
			return "", 0, fmt.Errorf("%s: %w", p, err)
		}
		// The blob's SHA is a hash of its content, so this also checks the
		// upload.
		if blob.SHA != sha {
			return "", 0, fmt.Errorf("%s: unexpected response creating the blob: %q, want %s", p, blob.SHA, sha)
		}
		entries = append(entries, record{"path": p, "mode": fileMode, "type": "blob", "sha": sha})
	}
	if len(entries) == 0 {
		return "", 0, fmt.Errorf("nothing to commit: the files are the same on branch %s", branch)
	}
	var tree apiSHA
	if err := api.send(ctx, http.MethodPost, repo.path("git", "trees"), record{"base_tree": head.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", 0, err
	}
	if !fullSHARE.MatchString(tree.SHA) {
		return "", 0, fmt.Errorf("unexpected response creating the tree: %q", tree.SHA)
	}
	var commit apiSHA
	body := record{"message": msg, "tree": tree.SHA, "parents": []string{parent}}
	if err := api.send(ctx, http.MethodPost, repo.path("git", "commits"), body, &commit); err != nil {
		return "", 0, err
	}
	if !fullSHARE.MatchString(commit.SHA) {
		return "", 0, fmt.Errorf("unexpected response creating the commit: %q", commit.SHA)
	}
	// Without force, the update fails if the branch is no longer at parent.
	if err := moveBranch(ctx, api, repo, branch, commit.SHA); err != nil {
		return "", 0, err
	}
	return commit.SHA, len(entries), nil
}

// moveBranch moves branch to commit without force. Only a 4xx says the
// branch wasn't moved: with no answer, or a 5xx, GitHub may have moved it all
// the same, so the branch is read back, and if it is at commit, it was moved.
func moveBranch(ctx context.Context, api *apiClient, repo repoRef, branch, commit string) error {
	body := record{"sha": commit, "force": false}
	err := api.send(ctx, http.MethodPatch, repo.path(withSegments(branch, "git", "refs", "heads")...), body, nil)
	if err == nil {
		return nil
	}
	// apiError's format.
	if !strings.HasPrefix(err.Error(), "HTTP 4") {
		var ref apiGitRef
		if _, rerr := api.get(ctx, repo.path(withSegments(branch, "git", "ref", "heads")...), nil, &ref); rerr != nil {
			return fmt.Errorf("%w; commit %s was made, and reading %s back failed too (%w), so it may or may not have been moved to it: check with git log", err, commit, branch, rerr)
		}
		if ref.Object.SHA == commit {
			return nil
		}
	}
	return fmt.Errorf("%w; commit %s was made but %s was not moved to it", err, commit, branch)
}
