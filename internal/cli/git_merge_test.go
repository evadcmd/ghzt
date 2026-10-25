package cli

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"testing"
)

const (
	theirsSHA  = "4444444444444444444444444444444444444444"
	mergeBase  = "5555555555555555555555555555555555555555"
	baseTreeID = "6666666666666666666666666666666666666666"
	theirsTree = "7777777777777777777777777777777777777777"
)

// mergeFiles are the files of each tree in mergeServer, path to mode and
// content; a missing path isn't in the tree.
var mergeFiles = map[string]map[string][2]string{
	baseTreeID: {
		"same.md":        {"100644", "s\n"},
		"theirs.md":      {"100644", "a\n"},
		"ours.md":        {"100644", "a\n"},
		"both.md":        {"100644", "a\n"},
		"docs/keep.md":   {"100644", "a\n"},
		"docs/theirs.md": {"100644", "a\n"},
		"deleted.md":     {"100644", "a\n"},
		"run.sh":         {"100644", "run\n"},
		"conflict.md":    {"100644", "a\n"},
		"gone.md":        {"100644", "a\n"},
	},
	// Branch feat/x.
	treeSHA: {
		"same.md":        {"100644", "s\n"},
		"theirs.md":      {"100644", "a\n"},
		"ours.md":        {"100644", "b\n"},
		"both.md":        {"100644", "b\n"},
		"docs/keep.md":   {"100644", "b\n"},
		"docs/theirs.md": {"100644", "b\n"},
		"deleted.md":     {"100644", "a\n"},
		"run.sh":         {"100644", "run\n"},
		"conflict.md":    {"100644", "b\n"},
		"gone.md":        {"100644", "b\n"},
	},
	// Branch topic.
	theirsTree: {
		"same.md":        {"100644", "s\n"},
		"theirs.md":      {"100644", "b\n"},
		"ours.md":        {"100644", "a\n"},
		"both.md":        {"100644", "b\n"},
		"docs/keep.md":   {"100644", "c\n"},
		"docs/theirs.md": {"100644", "c\n"},
		"added.md":       {"100644", "new\n"},
		"run.sh":         {"100755", "run\n"},
		"conflict.md":    {"100644", "c\n"},
	},
}

// treeListing is the recursive listing of the tree id in mergeFiles, with
// its directories.
func treeListing(id string) map[string]any {
	var entries []any
	dirs := map[string]bool{}
	for p, f := range mergeFiles[id] {
		entries = append(entries, map[string]any{"path": p, "mode": f[0], "type": "blob", "sha": sha(f[1])})
		for dir := path.Dir(p); dir != "." && !dirs[dir]; dir = path.Dir(dir) {
			dirs[dir] = true
			entries = append(entries, map[string]any{"path": dir, "mode": "040000", "type": "tree", "sha": dirSHA})
		}
	}
	return map[string]any{"sha": id, "tree": entries, "truncated": false}
}

// withMergeFiles makes mergeServer serve files instead of mergeFiles for the
// rest of the test.
func withMergeFiles(t *testing.T, files map[string]map[string][2]string) {
	t.Helper()
	old := mergeFiles
	mergeFiles = files
	t.Cleanup(func() { mergeFiles = old })
}

func apiReply(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

// mergeServer serves a repo with default branch main, where feat/x is at
// testSHA and topic at theirsSHA, with mergeBase as their merge base. The
// merges API conflicts.
func mergeServer(t *testing.T, got *[]request) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r", recorder(t, got, map[string]any{"default_branch": "main"}))
	mux.HandleFunc("POST /repos/o/r/merges", func(w http.ResponseWriter, r *http.Request) {
		recorder(t, got, nil)(discard{}, r)
		apiReply(http.StatusConflict, map[string]any{"message": "Merge conflict"})(w, r)
	})
	heads := map[string]string{"feat/x": testSHA, "topic": theirsSHA}
	mux.HandleFunc("GET /repos/o/r/git/ref/heads/{branch...}", func(w http.ResponseWriter, r *http.Request) {
		sha, ok := heads[r.PathValue("branch")]
		if !ok {
			recorder(t, got, nil)(discard{}, r)
			apiReply(http.StatusNotFound, map[string]any{"message": "Not Found"})(w, r)
			return
		}
		recorder(t, got, map[string]any{"object": map[string]any{"sha": sha, "type": "commit"}})(w, r)
	})
	mux.Handle("GET /repos/o/r/compare/"+testSHA+"..."+theirsSHA, recorder(t, got, map[string]any{
		"merge_base_commit": map[string]any{"sha": mergeBase},
	}))
	trees := map[string]string{mergeBase: baseTreeID, testSHA: treeSHA, theirsSHA: theirsTree}
	mux.HandleFunc("GET /repos/o/r/git/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		recorder(t, got, map[string]any{"sha": r.PathValue("sha"), "tree": map[string]any{"sha": trees[r.PathValue("sha")]}})(w, r)
	})
	mux.HandleFunc("GET /repos/o/r/git/trees/{sha}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("recursive") != "1" {
			t.Errorf("tree %s read without recursive=1", r.PathValue("sha"))
		}
		recorder(t, got, treeListing(r.PathValue("sha")))(w, r)
	})
	mux.Handle("POST /repos/o/r/git/trees", recorder(t, got, map[string]any{"sha": newTree}))
	mux.Handle("POST /repos/o/r/git/commits", recorder(t, got, map[string]any{"sha": commitSHA}))
	mux.Handle("PATCH /repos/o/r/git/refs/heads/", recorder(t, got, map[string]any{}))
	return mux
}

// discard is a ResponseWriter for a recorder whose reply is written
// separately.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// mergeReads are the requests mergeServer gets before a merge by tree
// writes anything.
var mergeReads = []request{
	{http.MethodGet, "/repos/o/r", nil},
	{http.MethodPost, "/repos/o/r/merges", map[string]any{
		"base": "feat/x", "head": "topic", "commit_message": "Merge branch 'topic' into feat/x\n",
	}},
	{http.MethodGet, "/repos/o/r/git/ref/heads/feat/x", nil},
	{http.MethodGet, "/repos/o/r/git/ref/heads/topic", nil},
	{http.MethodGet, "/repos/o/r/compare/" + testSHA + "..." + theirsSHA, nil},
	{http.MethodGet, "/repos/o/r/git/commits/" + mergeBase, nil},
	{http.MethodGet, "/repos/o/r/git/trees/" + baseTreeID, nil},
	{http.MethodGet, "/repos/o/r/git/commits/" + testSHA, nil},
	{http.MethodGet, "/repos/o/r/git/trees/" + treeSHA, nil},
	{http.MethodGet, "/repos/o/r/git/commits/" + theirsSHA, nil},
	{http.MethodGet, "/repos/o/r/git/trees/" + theirsTree, nil},
}

func TestGitMerge(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r", recorder(t, &got, map[string]any{"default_branch": "main"}))
	mux.HandleFunc("POST /repos/o/r/merges", func(w http.ResponseWriter, r *http.Request) {
		recorder(t, &got, nil)(discard{}, r)
		apiReply(http.StatusCreated, map[string]any{"sha": commitSHA})(w, r)
	})
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "feat/x", "-m", "Sync  ", "-m", "Body.", "topic")
	want := "[feat/x abcdef0] Sync\nhttps://github.com/o/r/commit/" + commitSHA + "\n"
	if code != exitOK || out != want || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodGet, "/repos/o/r", nil},
		{http.MethodPost, "/repos/o/r/merges", map[string]any{"base": "feat/x", "head": "topic", "commit_message": "Sync\n\nBody.\n"}},
	})
}

func TestGitMergeUpToDate(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r", reply(map[string]any{"default_branch": "main"}))
	mux.HandleFunc("POST /repos/o/r/merges", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	s := fakeGitHub(t, mux)

	// Resolutions aren't checked when there is no conflict.
	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "feat/x", "--ours", "a.md", "topic")
	if code != exitOK || out != "Already up to date.\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
}

func TestGitMergeByTree(t *testing.T) {
	var got []request
	s := fakeGitHub(t, mergeServer(t, &got))

	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "feat/x",
		"--ours", "docs/keep.md", "--theirs", "docs/theirs.md", "--theirs", "gone.md", "--ours", "conflict.md", "--ours", "conflict.md", "topic")
	want := "[feat/x abcdef0] Merge branch 'topic' into feat/x\n" +
		" resolved conflict.md with --ours\n" +
		" resolved docs/keep.md with --ours\n" +
		" resolved docs/theirs.md with --theirs\n" +
		" resolved gone.md with --theirs\n" +
		"https://github.com/o/r/commit/" + commitSHA + "\n"
	if code != exitOK || out != want || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	checkRequests(t, got, append(append([]request{}, mergeReads...),
		request{http.MethodPost, "/repos/o/r/git/trees", map[string]any{"base_tree": treeSHA, "tree": []any{
			map[string]any{"path": "added.md", "mode": "100644", "type": "blob", "sha": sha("new\n")},
			map[string]any{"path": "deleted.md", "mode": "100644", "type": "blob", "sha": nil},
			map[string]any{"path": "docs/theirs.md", "mode": "100644", "type": "blob", "sha": sha("c\n")},
			map[string]any{"path": "gone.md", "mode": "100644", "type": "blob", "sha": nil},
			map[string]any{"path": "run.sh", "mode": "100755", "type": "blob", "sha": sha("run\n")},
			map[string]any{"path": "theirs.md", "mode": "100644", "type": "blob", "sha": sha("b\n")},
		}}},
		request{http.MethodPost, "/repos/o/r/git/commits", map[string]any{
			"message": "Merge branch 'topic' into feat/x\n", "tree": newTree, "parents": []any{testSHA, theirsSHA},
		}},
		request{http.MethodPatch, "/repos/o/r/git/refs/heads/feat/x", map[string]any{"sha": commitSHA, "force": false}},
	))
}

func TestGitMergeByTreeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unresolved": {
			[]string{"--ours", "docs/keep.md"},
			"these paths changed on both feat/x and topic: conflict.md, docs/theirs.md, gone.md; run again with each given to --ours or --theirs",
		},
		"not a conflict": {
			[]string{"--ours", "docs/keep.md", "--theirs", "docs/theirs.md", "--theirs", "gone.md", "--ours", "conflict.md", "--theirs", "theirs.md", "--ours", "x.md"},
			"these paths are not conflicts: theirs.md, x.md; leave them out of --ours and --theirs",
		},
	} {
		var got []request
		s := fakeGitHub(t, mergeServer(t, &got))

		args := append([]string{"git", "merge", "-R", "o/r", "--branch", "feat/x"}, tc.args...)
		code, out, errOut := runWith(s, append(args, "topic")...)
		if code != 1 || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: got (%d, %q, %q), want %q", name, code, out, errOut, tc.want)
		}
		// Nothing is written.
		checkRequests(t, got, mergeReads)
	}
}

func TestGitMergeFileDirectory(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]map[string][2]string
		args  []string
		want  string
	}{
		// Theirs replaces docs/ with a file; ours adds a file in it.
		"file over directory": {
			map[string]map[string][2]string{
				baseTreeID: {"docs/keep.md": {"100644", "a\n"}},
				treeSHA:    {"docs/keep.md": {"100644", "a\n"}, "docs/new.md": {"100644", "n\n"}},
				theirsTree: {"docs": {"100644", "d\n"}},
			},
			nil,
			"these paths would be files with files under them: docs (with docs/new.md); make each one a file or a directory on feat/x with git commit, and run again",
		},
		// Ours edits x; theirs replaces it with x/y, which stays added when
		// x is resolved with --ours.
		"directory under file": {
			map[string]map[string][2]string{
				baseTreeID: {"x": {"100644", "a\n"}},
				treeSHA:    {"x": {"100644", "b\n"}},
				theirsTree: {"x/y": {"100644", "y\n"}},
			},
			[]string{"--ours", "x"},
			"these paths would be files with files under them: x (with x/y);",
		},
	} {
		t.Run(name, func(t *testing.T) {
			withMergeFiles(t, tc.files)
			var got []request
			s := fakeGitHub(t, mergeServer(t, &got))

			args := append([]string{"git", "merge", "-R", "o/r", "--branch", "feat/x"}, tc.args...)
			code, out, errOut := runWith(s, append(args, "topic")...)
			if code != 1 || out != "" || !strings.Contains(errOut, tc.want) {
				t.Fatalf("got (%d, %q, %q), want %q", code, out, errOut, tc.want)
			}
			// Nothing is written.
			checkRequests(t, got, mergeReads)
		})
	}
}

func TestGitMergeFileToDirectory(t *testing.T) {
	// With --theirs, x is deleted and x/y added: no file is under another.
	withMergeFiles(t, map[string]map[string][2]string{
		baseTreeID: {"x": {"100644", "a\n"}},
		treeSHA:    {"x": {"100644", "b\n"}},
		theirsTree: {"x/y": {"100644", "y\n"}},
	})
	var got []request
	s := fakeGitHub(t, mergeServer(t, &got))

	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "feat/x", "--theirs", "x", "topic")
	want := "[feat/x abcdef0] Merge branch 'topic' into feat/x\n" +
		" resolved x with --theirs\n" +
		"https://github.com/o/r/commit/" + commitSHA + "\n"
	if code != exitOK || out != want || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	checkRequests(t, got, append(append([]request{}, mergeReads...),
		request{http.MethodPost, "/repos/o/r/git/trees", map[string]any{"base_tree": treeSHA, "tree": []any{
			map[string]any{"path": "x", "mode": "100644", "type": "blob", "sha": nil},
			map[string]any{"path": "x/y", "mode": "100644", "type": "blob", "sha": sha("y\n")},
		}}},
		request{http.MethodPost, "/repos/o/r/git/commits", map[string]any{
			"message": "Merge branch 'topic' into feat/x\n", "tree": newTree, "parents": []any{testSHA, theirsSHA},
		}},
		request{http.MethodPatch, "/repos/o/r/git/refs/heads/feat/x", map[string]any{"sha": commitSHA, "force": false}},
	))
}

func TestGitMergeWriteFails(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("PATCH /repos/o/r/git/refs/heads/", apiReply(http.StatusUnprocessableEntity, map[string]any{"message": "Update is not a fast forward"}))
	mux.Handle("/", mergeServer(t, &got))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "feat/x",
		"--ours", "docs/keep.md", "--ours", "docs/theirs.md", "--ours", "gone.md", "--ours", "conflict.md", "topic")
	want := "Update is not a fast forward (PATCH /repos/o/r/git/refs/heads/feat/x); commit " + commitSHA + " was made but feat/x was not moved to it"
	if code != 1 || out != "" || !strings.Contains(errOut, want) {
		t.Fatalf("got (%d, %q, %q), want %q", code, out, errOut, want)
	}
}

func TestGitMergeMissingBranch(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r", reply(map[string]any{"default_branch": "main"}))
	mux.Handle("POST /repos/o/r/merges", apiReply(http.StatusNotFound, map[string]any{"message": "Base does not exist"}))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "nope", "topic")
	want := "HTTP 404: Base does not exist (POST /repos/o/r/merges); check that branches nope and topic exist"
	if code != 1 || out != "" || !strings.Contains(errOut, want) {
		t.Fatalf("got (%d, %q, %q), want %q", code, out, errOut, want)
	}
}

func TestGitMergeDefaultBranch(t *testing.T) {
	var got []request
	s := fakeGitHub(t, mergeServer(t, &got))

	code, out, errOut := runWith(s, "git", "merge", "-R", "o/r", "--branch", "main", "topic")
	want := "main is the default branch; merge into it with a pull request"
	if code != 1 || out != "" || !strings.Contains(errOut, want) {
		t.Fatalf("got (%d, %q, %q), want %q", code, out, errOut, want)
	}
	checkRequests(t, got, []request{{http.MethodGet, "/repos/o/r", nil}})
}

func TestGitMergeArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no branch":     {[]string{"topic"}, `required flag(s) "branch" not set`},
		"no head":       {[]string{"--branch", "feat/x"}, "accepts 1 arg(s), received 0"},
		"bad branch":    {[]string{"--branch", "feat/x.lock", "topic"}, `invalid branch name "feat/x.lock"`},
		"bad head":      {[]string{"--branch", "feat/x", "HEAD"}, `invalid branch name "HEAD"`},
		"bad path":      {[]string{"--branch", "feat/x", "--ours", "../a", "topic"}, "../a"},
		"both sides":    {[]string{"--branch", "feat/x", "--ours", "a.md", "--theirs", "a.md", "topic"}, "a.md is given to both --ours and --theirs"},
		"empty message": {[]string{"--branch", "feat/x", "-m", " \n", "topic"}, "aborting merge due to empty commit message"},
	} {
		// No request is made.
		s := fakeGitHub(t, http.NewServeMux())
		code, out, errOut := runWith(s, append([]string{"git", "merge", "-R", "o/r"}, tc.args...)...)
		if code == exitOK || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: got (%d, %q, %q), want %q", name, code, out, errOut, tc.want)
		}
	}
}
