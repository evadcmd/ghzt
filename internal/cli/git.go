package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/evadcmd/ghzt/internal/workspace"
)

const (
	// maxShowBytes is how much of a file or diff git show and git ls-tree
	// print. The rest is cut with a note on stderr, so stdout stays a prefix
	// of the real output; -o saves a whole file.
	maxShowBytes = 100_000
	// maxFileBytes is the largest file the contents API returns raw.
	maxFileBytes = 100 << 20
	// binarySniffLen is how far git looks for a NUL byte to call a file
	// binary.
	binarySniffLen = 8000
	// maxStatGraph is the widest +/- graph --stat draws.
	maxStatGraph = 50
	// gitDate is git's default date format.
	gitDate = "Mon Jan 2 15:04:05 2006 -0700"
)

// withSegments returns elems followed by the segments of p, a ref or repo
// path split at "/", so that each is escaped on its own and "feature/x"
// stays a branch name in the URL.
func withSegments(p string, elems ...string) []string {
	return append(elems, strings.Split(p, "/")...)
}

// pathFilter reports whether p is one of paths or under one of them; with no
// paths, everything matches.
func pathFilter(paths []string) func(p string) bool {
	return func(p string) bool {
		if len(paths) == 0 {
			return true
		}
		for _, want := range paths {
			if p == want || strings.HasPrefix(p, want+"/") {
				return true
			}
		}
		return false
	}
}

// checkPaths cleans and checks pathspecs: repo paths, a trailing "/" allowed.
func checkPaths(paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		p = strings.TrimSuffix(p, "/")
		if err := checkRepoPath(p); err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// quotePath quotes a path the way git does when it holds a quote, a
// backslash or a control character, so that it can't break a line apart.
func quotePath(p string) string {
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '"' || r == '\\' }) {
		return strconv.Quote(p)
	}
	return p
}

// writeCapped writes data, up to maxShowBytes of it, cut at a character
// boundary. If it is cut, a note on errOut says how much is left and hint
// says how to get it.
func writeCapped(out, errOut io.Writer, data []byte, hint string) error {
	n := len(data)
	if n > maxShowBytes {
		n = maxShowBytes
		for n > 0 && !utf8.RuneStart(data[n]) {
			n--
		}
	}
	if _, err := out.Write(data[:n]); err != nil {
		return err
	}
	if n < len(data) {
		_, _ = fmt.Fprintf(errOut, "… truncated, %d more bytes; %s\n", len(data)-n, hint)
	}
	return nil
}

func newGitShowCmd(s IO) *cobra.Command {
	var (
		stat   bool
		output string
	)
	cmd := &cobra.Command{
		Use:   "show [<commit> | <ref>:<path>] [-- <path>...]",
		Short: "Show a commit, or a file at a ref",
		Long: `Show a commit's message and diff, or the file at <ref>:<path>.

<commit> is a branch, tag or SHA, HEAD (the default branch) if omitted.
<ref>:<path> prints a file, or lists a directory; an empty <ref> is the
default branch. Paths after -- limit a commit's diff to those files and
directories.

Output stops after 100000 bytes, with a note on stderr. -o writes the whole
file under GHZT_ROOT instead.`,
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().BoolVar(&stat, "stat", false, "Show a diffstat instead of the diff")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Write the file at <ref>:<path> to `file` under GHZT_ROOT")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		objects, paths := args, []string(nil)
		if dash := c.ArgsLenAtDash(); dash >= 0 {
			objects, paths = args[:dash], args[dash:]
		}
		if len(objects) > 1 {
			return fmt.Errorf("expected one commit or <ref>:<path>, got %d; put paths after --", len(objects))
		}
		object := "HEAD"
		if len(objects) == 1 {
			object = objects[0]
		}
		paths, err := checkPaths(paths)
		if err != nil {
			return err
		}
		ref, file, isFile := strings.Cut(object, ":")
		// Only <ref>:<path> may leave the ref empty.
		if ref != "" || !isFile {
			if err := checkRef(ref); err != nil {
				return err
			}
		}
		if isFile {
			if stat || len(paths) > 0 {
				return errors.New("--stat and paths after -- only apply to a commit")
			}
			file = strings.TrimSuffix(file, "/")
			if err := checkRepoPath(file); err != nil {
				return err
			}
		} else if output != "" {
			return errors.New("-o only applies to <ref>:<path>")
		}
		// Open the root first, so that a bad GHZT_ROOT fails before the
		// download.
		var root *workspace.Root
		if output != "" {
			if root, err = workspace.FromEnv(s.getenv); err != nil {
				return err
			}
			defer func() { _ = root.Close() }()
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		if isFile {
			return showFile(ctx, s, api, repo.ref, object, ref, file, root, output)
		}
		commit, err := getCommit(ctx, api, repo.ref, ref)
		if err != nil {
			return err
		}
		return writeCommit(s, commit, pathFilter(paths), stat)
	}
	return cmd
}

// apiContent is an entry of a directory listing from the contents API.
type apiContent struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// showFile prints the file at ref:file, or lists the directory there, as git
// show does. With root, the file is written to output under it instead.
func showFile(ctx context.Context, s IO, api *apiClient, repo repoRef, object, ref, file string, root *workspace.Root, output string) error {
	q := url.Values{}
	if ref != "" && ref != "HEAD" {
		q.Set("ref", ref)
	}
	// raw+json: a file comes back raw, a directory as a JSON listing.
	data, h, err := api.getRaw(ctx, repo.path(withSegments(file, "contents")...), q, "application/vnd.github.raw+json", maxFileBytes)
	if err != nil {
		return err
	}
	if mt, _, _ := mime.ParseMediaType(h.Get("Content-Type")); mt == "application/json" {
		var entries []apiContent
		if err := json.Unmarshal(data, &entries); err != nil {
			// A submodule comes back as one JSON object.
			return fmt.Errorf("%s is not a file or directory", object)
		}
		if root != nil {
			return fmt.Errorf("%s is a directory", object)
		}
		var b bytes.Buffer
		fmt.Fprintf(&b, "tree %s\n\n", object)
		for _, e := range entries {
			name := e.Name
			if e.Type == "dir" {
				name += "/"
			}
			b.WriteString(quotePath(name) + "\n")
		}
		return writeCapped(s.Out, s.ErrOut, b.Bytes(), "list a subdirectory with git ls-tree")
	}
	if root != nil {
		if err := root.WriteFile(output, data); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(s.ErrOut, "wrote %d bytes to %s\n", len(data), output)
		return nil
	}
	if bytes.IndexByte(data[:min(len(data), binarySniffLen)], 0) >= 0 {
		return fmt.Errorf("%s is a binary file of %d bytes; save it with -o", object, len(data))
	}
	return writeCapped(s.Out, s.ErrOut, data, "save all of it with -o")
}

type (
	apiCommitFile struct {
		Filename         string  `json:"filename"`
		PreviousFilename string  `json:"previous_filename"`
		Status           string  `json:"status"`
		Additions        int     `json:"additions"`
		Deletions        int     `json:"deletions"`
		Patch            *string `json:"patch"`
	}

	apiCommitDetail struct {
		SHA    string `json:"sha"`
		Commit struct {
			Author struct {
				Name  string `json:"name"`
				Email string `json:"email"`
				Date  string `json:"date"`
			} `json:"author"`
			Message string `json:"message"`
		} `json:"commit"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Files []apiCommitFile `json:"files"`
	}
)

// getCommit fetches a commit with all its files, which the API pages (up to
// 3000 of them). Later pages are fetched by SHA, so a branch moving on in
// between can't mix two commits.
func getCommit(ctx context.Context, api *apiClient, repo repoRef, ref string) (apiCommitDetail, error) {
	var commit apiCommitDetail
	path := repo.path(withSegments(ref, "commits")...)
	for page := 1; ; page++ {
		var p apiCommitDetail
		h, err := api.get(ctx, path, url.Values{"page": {strconv.Itoa(page)}}, &p)
		if err != nil {
			return apiCommitDetail{}, err
		}
		if page == 1 {
			commit = p
			path = repo.path("commits", p.SHA)
		} else {
			commit.Files = append(commit.Files, p.Files...)
		}
		if !hasNextPage(h) || len(p.Files) == 0 {
			return commit, nil
		}
	}
}

// writeCommit prints a commit the way git show does: the header and
// message, then the diff or, with stat, the diffstat. Only the files keep
// accepts are shown.
func writeCommit(s IO, c apiCommitDetail, keep func(string) bool, stat bool) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "commit %s\n", c.SHA)
	if len(c.Parents) > 1 {
		short := make([]string, len(c.Parents))
		for i, p := range c.Parents {
			short[i] = p.SHA[:min(len(p.SHA), 7)]
		}
		fmt.Fprintf(&b, "Merge: %s\n", strings.Join(short, " "))
	}
	a := c.Commit.Author
	fmt.Fprintf(&b, "Author: %s <%s>\n", a.Name, a.Email)
	date := a.Date
	if t, err := time.Parse(time.RFC3339, a.Date); err == nil {
		date = t.Format(gitDate)
	}
	fmt.Fprintf(&b, "Date:   %s\n\n", date)
	for line := range strings.SplitSeq(strings.TrimRight(c.Commit.Message, "\n"), "\n") {
		b.WriteString(strings.TrimRight("    "+line, " ") + "\n")
	}
	var files []apiCommitFile
	for _, f := range c.Files {
		if keep(f.Filename) || (f.PreviousFilename != "" && keep(f.PreviousFilename)) {
			files = append(files, f)
		}
	}
	if len(files) > 0 {
		b.WriteString("\n")
	}
	if stat {
		writeStat(&b, files)
	} else {
		for _, f := range files {
			writeFileDiff(&b, f)
		}
	}
	return writeCapped(s.Out, s.ErrOut, b.Bytes(), "narrow it with -- <path>, or see the files with --stat")
}

// writeFileDiff writes one file's diff with git's headers. The API's patch
// has only the hunks, and no file modes, so there are no index or mode lines.
func writeFileDiff(b *bytes.Buffer, f apiCommitFile) {
	from, to := f.Filename, f.Filename
	if f.PreviousFilename != "" {
		from = f.PreviousFilename
	}
	fmt.Fprintf(b, "diff --git %s %s\n", quotePath("a/"+from), quotePath("b/"+to))
	if from != to {
		fmt.Fprintf(b, "rename from %s\nrename to %s\n", quotePath(from), quotePath(to))
	}
	if f.Patch == nil {
		if f.Additions+f.Deletions > 0 || f.Status != "renamed" {
			b.WriteString("(no patch from the API: binary file or diff too large)\n")
		}
		return
	}
	if *f.Patch == "" {
		return
	}
	oldName, newName := quotePath("a/"+from), quotePath("b/"+to)
	switch f.Status {
	case "added":
		oldName = "/dev/null"
	case "removed":
		newName = "/dev/null"
	}
	fmt.Fprintf(b, "--- %s\n+++ %s\n%s\n", oldName, newName, strings.TrimSuffix(*f.Patch, "\n"))
}

// writeStat writes git's --stat: a line per file with its changes and a +/-
// graph, scaled down to maxStatGraph if needed, then the totals.
func writeStat(b *bytes.Buffer, files []apiCommitFile) {
	names := make([]string, len(files))
	nameWidth, maxChanges, adds, dels := 0, 0, 0, 0
	for i, f := range files {
		names[i] = quotePath(f.Filename)
		if f.PreviousFilename != "" {
			names[i] = quotePath(f.PreviousFilename) + " => " + names[i]
		}
		nameWidth = max(nameWidth, utf8.RuneCountInString(names[i]))
		maxChanges = max(maxChanges, f.Additions+f.Deletions)
		adds += f.Additions
		dels += f.Deletions
	}
	countWidth := len(strconv.Itoa(maxChanges))
	for i, f := range files {
		plus, minus := f.Additions, f.Deletions
		if maxChanges > maxStatGraph {
			plus, minus = scaleStat(plus, maxChanges), scaleStat(minus, maxChanges)
		}
		pad := strings.Repeat(" ", nameWidth-utf8.RuneCountInString(names[i]))
		fmt.Fprintf(b, " %s%s | %*d", names[i], pad, countWidth, f.Additions+f.Deletions)
		// Like git, no space after a 0, which has no graph.
		if f.Additions+f.Deletions > 0 {
			b.WriteString(" " + strings.Repeat("+", plus) + strings.Repeat("-", minus))
		}
		b.WriteString("\n")
	}
	noun := "files"
	if len(files) == 1 {
		noun = "file"
	}
	fmt.Fprintf(b, " %d %s changed", len(files), noun)
	// git shows both counts when both are 0, and otherwise only the nonzero ones.
	if adds > 0 || dels == 0 {
		fmt.Fprintf(b, ", %d %s(+)", adds, plural(adds, "insertion"))
	}
	if dels > 0 || adds == 0 {
		fmt.Fprintf(b, ", %d %s(-)", dels, plural(dels, "deletion"))
	}
	b.WriteString("\n")
}

// scaleStat scales n changes to the graph, keeping at least one character
// for any change.
func scaleStat(n, maxChanges int) int {
	if n == 0 {
		return 0
	}
	return max(1, n*maxStatGraph/maxChanges)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

type apiTree struct {
	SHA  string `json:"sha"`
	Tree []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

func newGitLsTreeCmd(s IO) *cobra.Command {
	var recursive, nameOnly bool
	cmd := &cobra.Command{
		Use:   "ls-tree [-r] [--name-only] <tree-ish> [<path>...]",
		Short: "List the contents of a tree",
		Long: `List the files and directories in a tree: a branch, tag, commit or
tree SHA. -r lists every file under it, and then paths limit the listing to
those files and directories.

Output stops after 100000 bytes, with a note on stderr.`,
		Args: cobra.MinimumNArgs(1),
	}
	repo := addRepoFlag(cmd)
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recurse into subtrees")
	cmd.Flags().BoolVar(&nameOnly, "name-only", false, "List only the paths")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		ref := args[0]
		if err := checkRef(ref); err != nil {
			return err
		}
		paths, err := checkPaths(args[1:])
		if err != nil {
			return err
		}
		if len(paths) > 0 && !recursive {
			return errors.New("paths need -r")
		}
		ctx := c.Context()
		api, err := newAPIClient(ctx, s)
		if err != nil {
			return err
		}
		q := url.Values{}
		if recursive {
			q.Set("recursive", "1")
		}
		var tree apiTree
		if _, err := api.get(ctx, repo.ref.path(withSegments(ref, "git", "trees")...), q, &tree); err != nil {
			return err
		}
		keep := pathFilter(paths)
		var b bytes.Buffer
		for _, e := range tree.Tree {
			// git ls-tree -r lists the files, not the trees they are in.
			if (recursive && e.Type == "tree") || !keep(e.Path) {
				continue
			}
			if nameOnly {
				b.WriteString(quotePath(e.Path) + "\n")
			} else {
				fmt.Fprintf(&b, "%s %s %s\t%s\n", e.Mode, e.Type, e.SHA, quotePath(e.Path))
			}
		}
		if err := writeCapped(s.Out, s.ErrOut, b.Bytes(), "narrow it with paths"); err != nil {
			return err
		}
		if tree.Truncated {
			_, _ = fmt.Fprintln(s.ErrOut, "… truncated: the API returned part of the tree; list subtrees by their SHA")
		}
		return nil
	}
	return cmd
}
