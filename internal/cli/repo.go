package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// The names GitHub accepts, checked before they go into a URL path, so a
// value like "../.." can't point a request at another endpoint.
var (
	ownerRE    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
	repoNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	branchRE   = regexp.MustCompile(`^[^\s~^:?*\[\\]+$`)
)

const (
	maxOwnerLen    = 39
	maxRepoNameLen = 100
	maxBranchLen   = 255
)

// repoRef is an owner/repo pair.
type repoRef struct {
	owner, name string
}

func (r repoRef) String() string { return r.owner + "/" + r.name }

// path returns the API path of r, followed by elems, each escaped.
func (r repoRef) path(elems ...string) string {
	var b strings.Builder
	b.WriteString("/repos/" + url.PathEscape(r.owner) + "/" + url.PathEscape(r.name))
	for _, e := range elems {
		b.WriteString("/" + url.PathEscape(e))
	}
	return b.String()
}

func parseRepo(s string) (repoRef, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(name, "/") {
		return repoRef{}, fmt.Errorf(`expected "owner/repo", got %q`, s)
	}
	if err := checkOwner(owner); err != nil {
		return repoRef{}, err
	}
	if len(name) > maxRepoNameLen || !repoNameRE.MatchString(name) || name == "." || name == ".." {
		return repoRef{}, fmt.Errorf("invalid repository name %q", name)
	}
	return repoRef{owner: owner, name: name}, nil
}

func checkOwner(owner string) error {
	if len(owner) > maxOwnerLen || !ownerRE.MatchString(owner) {
		return fmt.Errorf("invalid owner %q", owner)
	}
	return nil
}

func checkBranch(branch string) error {
	if len(branch) > maxBranchLen || !branchRE.MatchString(branch) {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	return nil
}

// checkRef checks a branch, tag or commit SHA. Unlike a branch name, it is
// split at "/" into path segments, so none of them may be empty or "..".
// git's "HEAD~1"-style revisions are refused: the API doesn't take them.
func checkRef(ref string) error {
	if len(ref) > maxBranchLen || !branchRE.MatchString(ref) || strings.HasPrefix(ref, "-") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "//") {
		return fmt.Errorf("invalid ref %q", ref)
	}
	return nil
}

// checkRepoPath checks a path inside a repo, relative to its root, with no
// empty, "." or ".." segments.
func checkRepoPath(p string) error {
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path %q", p)
		}
	}
	return nil
}

// repoFlag is -R/--repo. It is a flag value so that a malformed repo is a
// flag error, reported before any request.
type repoFlag struct {
	ref repoRef
}

func (f *repoFlag) String() string {
	if f.ref.owner == "" {
		return ""
	}
	return f.ref.String()
}

func (f *repoFlag) Set(s string) error {
	ref, err := parseRepo(s)
	if err != nil {
		return err
	}
	f.ref = ref
	return nil
}

func (*repoFlag) Type() string { return "owner/repo" }

// addRepoFlag adds the required -R/--repo flag. There is no default repo:
// ghzt has no local checkout to take one from.
func addRepoFlag(cmd *cobra.Command) *repoFlag {
	f := &repoFlag{}
	cmd.Flags().VarP(f, "repo", "R", "Select a repository using the OWNER/REPO format")
	_ = cmd.MarkFlagRequired("repo")
	return f
}

// repoSliceFlag is a repeatable -R/--repo for the search commands, where it
// is optional.
type repoSliceFlag struct {
	refs []repoRef
}

func (f *repoSliceFlag) String() string {
	names := make([]string, len(f.refs))
	for i, r := range f.refs {
		names[i] = r.String()
	}
	return "[" + strings.Join(names, ",") + "]"
}

func (f *repoSliceFlag) Set(s string) error {
	for v := range strings.SplitSeq(s, ",") {
		ref, err := parseRepo(v)
		if err != nil {
			return err
		}
		f.refs = append(f.refs, ref)
	}
	return nil
}

func (*repoSliceFlag) Type() string { return "strings" }
