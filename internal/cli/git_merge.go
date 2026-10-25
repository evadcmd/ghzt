package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// mergeSide is how a conflict is resolved: "ours" keeps the version on the
// branch merged into, "theirs" takes the one on the branch merged in.
type mergeSide string

const (
	sideOurs   mergeSide = "ours"
	sideTheirs mergeSide = "theirs"
)

// mergeResolutions checks the paths given to --ours and --theirs and maps
// each to its side.
func mergeResolutions(ours, theirs []string) (map[string]mergeSide, error) {
	res := map[string]mergeSide{}
	for side, paths := range map[mergeSide][]string{sideOurs: ours, sideTheirs: theirs} {
		for _, p := range paths {
			if err := checkRepoPath(p); err != nil {
				return nil, err
			}
			if s, ok := res[p]; ok && s != side {
				return nil, fmt.Errorf("%s is given to both --ours and --theirs", p)
			}
			res[p] = side
		}
	}
	return res, nil
}

func newGitMergeCmd(s IO) *cobra.Command {
	var (
		messages, ours, theirs []string
		branch                 string
	)
	cmd := &cobra.Command{
		Use:   "merge --branch <branch> [-m <msg>] [--ours <path>]... [--theirs <path>]... <head>",
		Short: "Merge a branch into another",
		Long: `Merge the branch <head> into --branch on GitHub, with a merge commit. Unlike
git, there is no checkout: --branch names the branch to merge into, which
can't be the default branch; merge into that with a pull request.

GitHub merges the lines when it can. If it reports a conflict, the merge is
made path by path instead: a path changed on only one side takes that side's
version, and a path changed differently on both sides is a conflict, even if
git could combine its lines. Conflicts are listed in the error, and nothing
is written; run the command again with each one given to --ours, to keep the
version on --branch, or --theirs, to take the one on <head>. To keep changes
from both, resolve with one side and fix the file with git commit
afterwards. A path given to --ours or --theirs that isn't a conflict is an
error. If the branches merge without a conflict, --ours and --theirs are not
needed and not used.

The branch is moved without force, so if it moved on during the merge, the
update fails and the branch is left as it was.

The message is "Merge branch '<head>' into <branch>" unless -m is given;
several -m are joined as paragraphs.

Prints "Already up to date." if --branch already has <head>. Otherwise
prints "[<branch> <short sha>] <subject>", the paths resolved, and the
commit's URL.`,
		Args: cobra.ExactArgs(1),
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().StringVar(&branch, "branch", "", "The branch to merge into")
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "Use the given `msg` as the commit message; several are joined as paragraphs")
	cmd.Flags().StringArrayVar(&ours, "ours", nil, "Resolve a conflict at `path` with the version on --branch")
	cmd.Flags().StringArrayVar(&theirs, "theirs", nil, "Resolve a conflict at `path` with the version on <head>")
	_ = cmd.MarkFlagRequired("branch")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		head := args[0]
		if err := checkNewBranch(branch); err != nil {
			return err
		}
		if err := checkNewBranch(head); err != nil {
			return err
		}
		resolutions, err := mergeResolutions(ours, theirs)
		if err != nil {
			return err
		}
		msg := cleanMessage(strings.Join(messages, "\n\n"))
		if !c.Flags().Changed("message") {
			msg = fmt.Sprintf("Merge branch '%s' into %s\n", head, branch)
		} else if msg == "" {
			return errors.New("aborting merge due to empty commit message")
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		m := &merger{api: api, repo: repo.ref, base: branch, head: head}
		sha, resolved, err := m.merge(ctx, msg, resolutions)
		if err != nil {
			return err
		}
		if sha == "" {
			_, err := fmt.Fprintln(s.Out, "Already up to date.")
			return err
		}
		para, _, _ := strings.Cut(strings.TrimSuffix(msg, "\n"), "\n\n")
		subject := strings.ReplaceAll(para, "\n", " ")
		if _, err := fmt.Fprintf(s.Out, "[%s %s] %s\n", branch, sha[:7], subject); err != nil {
			return err
		}
		for _, p := range resolved {
			if _, err := fmt.Fprintf(s.Out, " resolved %s with --%s\n", p, resolutions[p]); err != nil {
				return err
			}
		}
		return writeURL(s.Out, "https://github.com/"+repo.ref.String()+"/commit/"+sha)
	}
	return cmd
}

// merger merges the branch head into the branch base.
type merger struct {
	api        *apiClient
	repo       repoRef
	base, head string
}

// merge merges with msg and returns the merge commit's SHA, empty if base
// already has head, and the conflicts resolved, sorted.
func (m *merger) merge(ctx context.Context, msg string, resolutions map[string]mergeSide) (string, []string, error) {
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := m.api.get(ctx, m.repo.path(), nil, &repo); err != nil {
		return "", nil, err
	}
	if m.base == repo.DefaultBranch {
		return "", nil, fmt.Errorf("%s is the default branch; merge into it with a pull request (gh pr create)", m.base)
	}
	sha, conflict, err := m.mergeAPI(ctx, msg)
	if err != nil || !conflict {
		return sha, nil, err
	}
	// The merges API takes no resolutions, so the merge commit is built
	// from the trees, path by path.
	return m.mergeByTree(ctx, msg, resolutions)
}

// mergeAPI merges with the merges API, and reports a conflict rather than
// returning it as an error. send can't tell a 201 from a 204, which here
// means base already has head, so this makes the request itself.
func (m *merger) mergeAPI(ctx context.Context, msg string) (string, bool, error) {
	path := m.repo.path("merges")
	data, err := json.Marshal(record{"base": m.base, "head": m.head, "commit_message": msg})
	if err != nil {
		return "", false, err
	}
	resp, err := m.api.request(ctx, http.MethodPost, path, nil, "application/vnd.github+json", bytes.NewReader(data))
	if err != nil {
		// apiError's format.
		switch {
		case strings.HasPrefix(err.Error(), "HTTP 409:"):
			return "", true, nil
		case strings.HasPrefix(err.Error(), "HTTP 404:"):
			return "", false, fmt.Errorf("%w; check that branches %s and %s exist", err, m.base, m.head)
		}
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return "", false, nil
	}
	var commit apiSHA
	if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
		return "", false, fmt.Errorf("decode response to %s %s: %w", http.MethodPost, path, err)
	}
	if !fullSHARE.MatchString(commit.SHA) {
		return "", false, fmt.Errorf("unexpected response merging: %q", commit.SHA)
	}
	return commit.SHA, false, nil
}

// mergeByTree makes the merge commit from the trees of the two heads and
// their merge base: each path takes head's version if only head changed it,
// and a path both changed, differently, takes the side resolutions give it.
// Every conflict and resolution is checked before anything is written.
func (m *merger) mergeByTree(ctx context.Context, msg string, resolutions map[string]mergeSide) (string, []string, error) {
	ours, err := m.branchSHA(ctx, m.base)
	if err != nil {
		return "", nil, err
	}
	theirs, err := m.branchSHA(ctx, m.head)
	if err != nil {
		return "", nil, err
	}
	var compare struct {
		MergeBase apiSHA `json:"merge_base_commit"`
	}
	if _, err := m.api.get(ctx, m.repo.path("compare", ours+"..."+theirs), nil, &compare); err != nil {
		return "", nil, err
	}
	mergeBase := compare.MergeBase.SHA
	if !fullSHARE.MatchString(mergeBase) {
		return "", nil, fmt.Errorf("unexpected response comparing %s and %s: merge base %q", m.base, m.head, mergeBase)
	}
	_, old, err := m.treeFiles(ctx, mergeBase)
	if err != nil {
		return "", nil, err
	}
	ourTree, mine, err := m.treeFiles(ctx, ours)
	if err != nil {
		return "", nil, err
	}
	_, next, err := m.treeFiles(ctx, theirs)
	if err != nil {
		return "", nil, err
	}

	paths := map[string]bool{}
	for _, files := range []map[string]treeEntry{old, mine, next} {
		for p := range files {
			paths[p] = true
		}
	}
	var conflicts, resolved []string
	var entries []record
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		o, inOld := old[p]
		y, inMine := mine[p]
		t, inNext := next[p]
		same := func(a treeEntry, inA bool, b treeEntry, inB bool) bool { return inA == inB && a == b }
		// Unchanged on head, or the same change on both sides: ours is right.
		if same(t, inNext, o, inOld) || same(t, inNext, y, inMine) {
			continue
		}
		if !same(y, inMine, o, inOld) {
			side, ok := resolutions[p]
			if !ok {
				conflicts = append(conflicts, p)
				continue
			}
			resolved = append(resolved, p)
			if side == sideOurs {
				continue
			}
		}
		switch {
		case inNext:
			entries = append(entries, record{"path": p, "mode": t.Mode, "type": t.Type, "sha": t.SHA})
		case inMine:
			// Head deleted the path. A null SHA deletes it; mode and type
			// must still match it.
			entries = append(entries, record{"path": p, "mode": y.Mode, "type": y.Type, "sha": nil})
		}
	}
	if len(conflicts) > 0 {
		return "", nil, fmt.Errorf("these paths changed on both %s and %s: %s; run again with each given to --ours or --theirs",
			m.base, m.head, strings.Join(conflicts, ", "))
	}
	var unused []string
	for p := range resolutions {
		if !slices.Contains(resolved, p) {
			unused = append(unused, p)
		}
	}
	if len(unused) > 0 {
		slices.Sort(unused)
		return "", nil, fmt.Errorf("these paths are not conflicts: %s; leave them out of --ours and --theirs", strings.Join(unused, ", "))
	}

	tree := ourTree
	if len(entries) > 0 {
		var t apiSHA
		if err := m.api.send(ctx, http.MethodPost, m.repo.path("git", "trees"), record{"base_tree": ourTree, "tree": entries}, &t); err != nil {
			return "", nil, err
		}
		if !fullSHARE.MatchString(t.SHA) {
			return "", nil, fmt.Errorf("unexpected response creating the tree: %q", t.SHA)
		}
		tree = t.SHA
	}
	var commit apiSHA
	body := record{"message": msg, "tree": tree, "parents": []string{ours, theirs}}
	if err := m.api.send(ctx, http.MethodPost, m.repo.path("git", "commits"), body, &commit); err != nil {
		return "", nil, err
	}
	if !fullSHARE.MatchString(commit.SHA) {
		return "", nil, fmt.Errorf("unexpected response creating the commit: %q", commit.SHA)
	}
	// Without force, the update fails if the branch is no longer at ours.
	body = record{"sha": commit.SHA, "force": false}
	if err := m.api.send(ctx, http.MethodPatch, m.repo.path(withSegments(m.base, "git", "refs", "heads")...), body, nil); err != nil {
		return "", nil, fmt.Errorf("%w; commit %s was made but %s was not moved to it", err, commit.SHA, m.base)
	}
	return commit.SHA, resolved, nil
}

// branchSHA returns the commit branch is at.
func (m *merger) branchSHA(ctx context.Context, branch string) (string, error) {
	var ref apiGitRef
	if _, err := m.api.get(ctx, m.repo.path(withSegments(branch, "git", "ref", "heads")...), nil, &ref); err != nil {
		return "", err
	}
	if ref.Object.Type != "commit" || !fullSHARE.MatchString(ref.Object.SHA) {
		return "", fmt.Errorf("unexpected response resolving branch %s: %s %q", branch, ref.Object.Type, ref.Object.SHA)
	}
	return ref.Object.SHA, nil
}

// treeFiles returns the SHA of commit's tree and every entry in it that
// isn't a directory, by path. A tree too large to list in one response is an
// error: a path missing from it might still be there.
func (m *merger) treeFiles(ctx context.Context, commit string) (string, map[string]treeEntry, error) {
	var c apiGitCommit
	if _, err := m.api.get(ctx, m.repo.path("git", "commits", commit), nil, &c); err != nil {
		return "", nil, err
	}
	if !fullSHARE.MatchString(c.Tree.SHA) {
		return "", nil, fmt.Errorf("unexpected response reading commit %s: tree %q", commit, c.Tree.SHA)
	}
	var tree apiTree
	if _, err := m.api.get(ctx, m.repo.path("git", "trees", c.Tree.SHA), url.Values{"recursive": {"1"}}, &tree); err != nil {
		return "", nil, err
	}
	if tree.Truncated {
		return "", nil, fmt.Errorf("the tree of %s is too large to merge path by path", commit)
	}
	files := make(map[string]treeEntry, len(tree.Tree))
	for _, e := range tree.Tree {
		if e.Type != "tree" {
			files[e.Path] = treeEntry{Mode: e.Mode, Type: e.Type, SHA: e.SHA}
		}
	}
	return c.Tree.SHA, files, nil
}
