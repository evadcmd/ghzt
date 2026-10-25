package cli

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	treeSHA   = "1111111111111111111111111111111111111111"
	newTree   = "2222222222222222222222222222222222222222"
	dirSHA    = "3333333333333333333333333333333333333333"
	commitSHA = "abcdef0123456789abcdef0123456789abcdef01"
)

// sha is blobSHA of a string.
func sha(s string) string { return blobSHA([]byte(s)) }

// baseTrees are the trees of the commit at testSHA, by SHA:
//
//	README.md   100644 "old\n"
//	docs/a.md   100644 "a\n"
//	docs/run.sh 100644 "run\n"
//	lib         a submodule
func baseTrees() map[string]any {
	return map[string]any{
		treeSHA: map[string]any{"sha": treeSHA, "tree": []any{
			map[string]any{"path": "README.md", "mode": "100644", "type": "blob", "sha": sha("old\n")},
			map[string]any{"path": "docs", "mode": "040000", "type": "tree", "sha": dirSHA},
			map[string]any{"path": "lib", "mode": "160000", "type": "commit", "sha": testSHA},
		}},
		dirSHA: map[string]any{"sha": dirSHA, "tree": []any{
			map[string]any{"path": "a.md", "mode": "100644", "type": "blob", "sha": sha("a\n")},
			map[string]any{"path": "run.sh", "mode": "100644", "type": "blob", "sha": sha("run\n")},
		}},
	}
}

// commitServer serves a repo whose branch main is at testSHA, with the trees
// of baseTrees, recording the requests in got. A blob gets the SHA of its
// content, as on GitHub.
func commitServer(t *testing.T, got *[]request) *http.ServeMux {
	t.Helper()
	trees := baseTrees()
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/git/ref/heads/", recorder(t, got, map[string]any{
		"object": map[string]any{"sha": testSHA, "type": "commit"},
	}))
	mux.Handle("GET /repos/o/r/git/commits/", recorder(t, got, map[string]any{
		"sha": testSHA, "tree": map[string]any{"sha": treeSHA},
	}))
	mux.HandleFunc("GET /repos/o/r/git/trees/{sha}", func(w http.ResponseWriter, r *http.Request) {
		tree, ok := trees[r.PathValue("sha")]
		if !ok {
			t.Errorf("unknown tree %s", r.PathValue("sha"))
			http.NotFound(w, r)
			return
		}
		recorder(t, got, tree)(w, r)
	})
	mux.HandleFunc("POST /repos/o/r/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		recorder(t, got, nil)(httptest.NewRecorder(), r)
		content, _ := (*got)[len(*got)-1].Body["content"].(string)
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			t.Errorf("blob content: %v", err)
		}
		reply(map[string]any{"sha": blobSHA(data)})(w, r)
	})
	mux.Handle("POST /repos/o/r/git/trees", recorder(t, got, map[string]any{"sha": newTree}))
	mux.Handle("POST /repos/o/r/git/commits", recorder(t, got, map[string]any{"sha": commitSHA}))
	mux.Handle("PATCH /repos/o/r/git/refs/heads/", recorder(t, got, map[string]any{}))
	return mux
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestBlobSHA(t *testing.T) {
	// git hash-object of an empty file and of "hello\n".
	for in, want := range map[string]string{
		"":        "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		"hello\n": "ce013625030ba8dba906f756967f9e9ca394464a",
	} {
		if got := sha(in); got != want {
			t.Errorf("blobSHA(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestGitCommit(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"README.md": "hi\n", "scripts/run.sh": "#!/bin/sh\n"})
	if err := os.Chmod(filepath.Join(root, "scripts", "run.sh"), 0o700); err != nil { //nolint:gosec // G302: the test needs an executable file.
		t.Fatal(err)
	}

	code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "feat/x",
		"-m", "Add things  ", "-m", "More detail.\nSecond line.\n\n\n", "--", "README.md", "scripts/run.sh", "README.md")
	want := "[feat/x abcdef0] Add things\n 2 files changed\nhttps://github.com/o/r/commit/" + commitSHA + "\n"
	if code != exitOK || out != want || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodGet, "/repos/o/r/git/ref/heads/feat/x", nil},
		{http.MethodGet, "/repos/o/r/git/commits/" + testSHA, nil},
		// The root tree is read once, for both files.
		{http.MethodGet, "/repos/o/r/git/trees/" + treeSHA, nil},
		{http.MethodPost, "/repos/o/r/git/blobs", map[string]any{"content": b64("hi\n"), "encoding": "base64"}},
		{http.MethodPost, "/repos/o/r/git/blobs", map[string]any{"content": b64("#!/bin/sh\n"), "encoding": "base64"}},
		{http.MethodPost, "/repos/o/r/git/trees", map[string]any{"base_tree": treeSHA, "tree": []any{
			map[string]any{"path": "README.md", "mode": "100644", "type": "blob", "sha": sha("hi\n")},
			map[string]any{"path": "scripts/run.sh", "mode": "100755", "type": "blob", "sha": sha("#!/bin/sh\n")},
		}}},
		{http.MethodPost, "/repos/o/r/git/commits", map[string]any{
			"message": "Add things\n\nMore detail.\nSecond line.\n", "tree": newTree, "parents": []any{testSHA},
		}},
		{http.MethodPatch, "/repos/o/r/git/refs/heads/feat/x", map[string]any{"sha": commitSHA, "force": false}},
	})
}

func TestGitCommitRemove(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	// README.md is unchanged, and docs/run.sh only becomes executable.
	writeFiles(t, root, map[string]string{"README.md": "old\n", "docs/run.sh": "run\n"})
	if err := os.Chmod(filepath.Join(root, "docs", "run.sh"), 0o700); err != nil { //nolint:gosec // G302: the test needs an executable file.
		t.Fatal(err)
	}

	code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-m", "Tidy",
		"--rm", "docs/a.md", "--rm", "lib", "--rm", "docs/a.md", "README.md", "docs/run.sh")
	want := "[main abcdef0] Tidy\n 3 files changed\nhttps://github.com/o/r/commit/" + commitSHA + "\n"
	if code != exitOK || out != want || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	// No blob is uploaded: run.sh's is on GitHub already.
	checkRequests(t, got, []request{
		{http.MethodGet, "/repos/o/r/git/ref/heads/main", nil},
		{http.MethodGet, "/repos/o/r/git/commits/" + testSHA, nil},
		{http.MethodGet, "/repos/o/r/git/trees/" + treeSHA, nil},
		{http.MethodGet, "/repos/o/r/git/trees/" + dirSHA, nil},
		{http.MethodPost, "/repos/o/r/git/trees", map[string]any{"base_tree": treeSHA, "tree": []any{
			map[string]any{"path": "docs/a.md", "mode": "100644", "type": "blob", "sha": nil},
			map[string]any{"path": "lib", "mode": "160000", "type": "commit", "sha": nil},
			map[string]any{"path": "docs/run.sh", "mode": "100755", "type": "blob", "sha": sha("run\n")},
		}}},
		{http.MethodPost, "/repos/o/r/git/commits", map[string]any{
			"message": "Tidy\n", "tree": newTree, "parents": []any{testSHA},
		}},
		{http.MethodPatch, "/repos/o/r/git/refs/heads/main", map[string]any{"sha": commitSHA, "force": false}},
	})
}

func TestGitCommitNothing(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"README.md": "old\n", "docs/a.md": "a\n", "docs/new.md": "new\n"})

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"README.md", "docs/a.md"}, "nothing to commit: the files are the same on branch main"},
		{[]string{"--rm", "missing.md", "README.md"}, "--rm missing.md: no such file on branch main"},
		{[]string{"--rm", "README.md/x"}, "--rm README.md/x: no such file on branch main"},
		{[]string{"--rm", "lib/x"}, "--rm lib/x: no such file on branch main"},
		{[]string{"--rm", "docs/new.md"}, "--rm docs/new.md: no such file on branch main"},
		{[]string{"--rm", "docs"}, "--rm docs: a directory on branch main; give its files"},
		{[]string{"docs/new.md", "--rm", "docs"}, "--rm docs: a directory on branch main"},
	} {
		got = nil
		code, out, errOut := runWith(s, append([]string{"git", "commit", "-R", "o/r", "--branch", "main", "-m", "x"}, tc.args...)...)
		if code != 1 || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: got (%d, %q, %q), want %q", tc.args, code, out, errOut, tc.want)
		}
		for _, r := range got {
			if r.Method != http.MethodGet {
				t.Errorf("%q: made %s %s", tc.args, r.Method, r.Path)
			}
		}
	}
}

func TestGitCommitOverDirectory(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"docs": "not a directory\n"})
	code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-m", "x", "docs")
	if code != 1 || out != "" || !strings.Contains(errOut, "docs: a directory on branch main") {
		t.Errorf("got (%d, %q, %q)", code, out, errOut)
	}
}

func TestGitCommitUnderFile(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"README.md/x": "x\n", "lib/x": "x\n"})

	for p, want := range map[string]string{
		"README.md/x": "README.md/x: README.md is not a directory on branch main",
		"lib/x":       "lib/x: lib is not a directory on branch main",
	} {
		got = nil
		code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-m", "x", p)
		if code != 1 || out != "" || !strings.Contains(errOut, want) {
			t.Errorf("%s: got (%d, %q, %q), want %q", p, code, out, errOut, want)
		}
		for _, r := range got {
			if r.Method != http.MethodGet {
				t.Errorf("%s: made %s %s", p, r.Method, r.Path)
			}
		}
	}
}

func TestGitCommitBadTree(t *testing.T) {
	for name, tc := range map[string]struct {
		tree map[string]any
		want string
	}{
		"truncated": {map[string]any{"tree": []any{}, "truncated": true}, "tree " + treeSHA + " is too large to read"},
		"bad subtree": {map[string]any{"tree": []any{
			map[string]any{"path": "docs", "mode": "040000", "type": "tree", "sha": "../x"},
		}}, `unexpected response reading a tree: SHA "../x"`},
	} {
		var got []request
		mux := http.NewServeMux()
		mux.Handle("GET /repos/o/r/git/trees/"+treeSHA, reply(tc.tree))
		mux.Handle("/", commitServer(t, &got))
		s := fakeGitHub(t, mux)
		withRoot(t, &s)

		code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-m", "x", "--rm", "docs/a.md")
		if code != 1 || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: got (%d, %q, %q), want %q", name, code, out, errOut, tc.want)
		}
	}
}

func TestCleanMessage(t *testing.T) {
	for in, want := range map[string]string{
		"":                                       "",
		" \n\t\n":                                "",
		"subject":                                "subject\n",
		"\n\nsubject  \r\n\n\n\nbody\nmore \n\n": "subject\n\nbody\nmore\n",
		"a\n \nb\nc\n\n\nd":                      "a\n\nb\nc\n\nd\n",
	} {
		if got := cleanMessage(in); got != want {
			t.Errorf("cleanMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitCommitMessageFile(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"a.txt": "a", "msg.txt": "\n\nFix it\t\n\n\nBody\n"})

	code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-F", "msg.txt", "a.txt")
	if code != exitOK || !strings.HasPrefix(out, "[main abcdef0] Fix it\n 1 file changed\n") || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if msg := got[len(got)-2].Body["message"]; msg != "Fix it\n\nBody\n" {
		t.Errorf("message = %q", msg)
	}

	got = nil
	s.In = strings.NewReader("From stdin\n")
	code, out, errOut = runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-F", "-", "a.txt")
	if code != exitOK || !strings.HasPrefix(out, "[main abcdef0] From stdin\n") || errOut != "" {
		t.Fatalf("stdin: got (%d, %q, %q)", code, out, errOut)
	}

	// Like git, the subject is the whole first paragraph.
	s.In = strings.NewReader("Wrapped\nsubject\n\nBody\n")
	code, out, errOut = runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-F", "-", "a.txt")
	if code != exitOK || !strings.HasPrefix(out, "[main abcdef0] Wrapped subject\n") || errOut != "" {
		t.Fatalf("paragraph: got (%d, %q, %q)", code, out, errOut)
	}
}

func TestGitCommitInvalid(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"a.txt": "a", "empty.txt": " \n\t\n"})
	tooMany := make([]string, maxCommitFiles+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint("f", i)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-m", "x", "a.txt"}, `"branch" not set`},
		{[]string{"--branch", "main", "a.txt"}, "at least one of the flags"},
		{[]string{"--branch", "main", "-m", "x", "-F", "a.txt", "a.txt"}, "were all set"},
		{[]string{"--branch", "main", "-m", "x"}, "nothing to commit: give a <path> or --rm"},
		{[]string{"--branch", "main", "-m", "x", "--rm", "a.txt", "a.txt"}, "a.txt is both committed and removed"},
		{[]string{"--branch", "main", "-m", "x", "--rm", "docs/../a.txt"}, "invalid path"},
		{append([]string{"--branch", "main", "-m", "x", "--rm", "a.txt", "--"}, tooMany[1:]...), "101 files to commit, the maximum is 100"},
		{[]string{"--branch", "a..b", "-m", "x", "a.txt"}, "invalid branch name"},
		{[]string{"--branch", "HEAD", "-m", "x", "a.txt"}, "invalid branch name"},
		{[]string{"--branch", "main", "-m", "x", "../a.txt"}, "invalid path"},
		{[]string{"--branch", "main", "-m", "x", "./a.txt"}, "invalid path"},
		{[]string{"--branch", "main", "-m", "x", "/a.txt"}, "invalid path"},
		{[]string{"--branch", "main", "-m", " \n\n", "a.txt"}, "empty commit message"},
		{[]string{"--branch", "main", "-F", "empty.txt", "a.txt"}, "empty commit message"},
		{[]string{"--branch", "main", "-F", "missing.txt", "a.txt"}, "no such file"},
		{append([]string{"--branch", "main", "-m", "x", "--"}, tooMany...), "101 files to commit, the maximum is 100"},
	} {
		code, out, errOut := runWith(s, append([]string{"git", "commit", "-R", "o/r"}, tc.args...)...)
		if code != 1 || out != "" || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: got (%d, %q, %q), want %q", tc.args, code, out, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("made requests: %+v", got)
	}
}

func TestGitCommitFileErrors(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"a.txt": "a"})
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"link.txt": "not a regular file", "missing.txt": "no such file"} {
		got = nil
		code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-m", "x", "a.txt", name)
		if code != 1 || out != "" || !strings.Contains(errOut, want) {
			t.Errorf("%s: got (%d, %q, %q)", name, code, out, errOut)
		}
		// Nothing past the blobs: no tree, commit or ref update.
		for _, r := range got {
			if r.Method == http.MethodPatch || strings.HasSuffix(r.Path, "/trees") || r.Path == "/repos/o/r/git/commits" {
				t.Errorf("%s: made %s %s", name, r.Method, r.Path)
			}
		}
	}
}

func TestGitCommitAPIErrors(t *testing.T) {
	fail := func(status int, msg string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"` + msg + `"}`))
		}
	}
	var got []request
	mux := commitServer(t, &got)
	mux.Handle("GET /repos/o/r/git/ref/heads/gone", fail(http.StatusNotFound, "Not Found"))
	mux.Handle("GET /repos/o/r/git/ref/heads/odd", reply(map[string]any{
		"object": map[string]any{"sha": testSHA, "type": "tag"},
	}))
	mux.Handle("PATCH /repos/o/r/git/refs/heads/moved", fail(http.StatusUnprocessableEntity, "Update is not a fast forward"))
	s := fakeGitHub(t, mux)
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"a.txt": "a"})

	for branch, want := range map[string]string{
		"gone":  "HTTP 404: Not Found (GET /repos/o/r/git/ref/heads/gone); if branch gone doesn't exist, create it with git branch",
		"odd":   "unexpected response resolving branch odd: tag",
		"moved": "Update is not a fast forward (PATCH /repos/o/r/git/refs/heads/moved); commit " + commitSHA + " was made but moved was not moved to it",
	} {
		code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", branch, "-m", "x", "a.txt")
		if code != 1 || out != "" || !strings.Contains(errOut, want) {
			t.Errorf("%s: got (%d, %q, %q), want %q", branch, code, out, errOut, want)
		}
	}
}

func TestGitCommitBadSHA(t *testing.T) {
	for path, want := range map[string]string{
		"GET /repos/o/r/git/commits/": `unexpected response reading commit ` + testSHA + `: tree ""`,
		"POST /repos/o/r/git/blobs":   `a.txt: unexpected response creating the blob: ""`,
		"POST /repos/o/r/git/trees":   `unexpected response creating the tree: ""`,
	} {
		var got []request
		mux := http.NewServeMux()
		// The more specific pattern wins: this response has no SHA.
		mux.Handle(path, reply(map[string]any{}))
		mux.Handle("/", commitServer(t, &got))
		s := fakeGitHub(t, mux)
		root := withRoot(t, &s)
		writeFiles(t, root, map[string]string{"a.txt": "a"})

		code, out, errOut := runWith(s, "git", "commit", "-R", "o/r", "--branch", "main", "-m", "x", "a.txt")
		if code != 1 || out != "" || !strings.Contains(errOut, want) {
			t.Errorf("%s: got (%d, %q, %q), want %q", path, code, out, errOut, want)
		}
		for _, r := range got {
			if r.Method == http.MethodPatch {
				t.Errorf("%s: moved the branch", path)
			}
		}
	}
}
