package cli

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	treeSHA   = "1111111111111111111111111111111111111111"
	newTree   = "2222222222222222222222222222222222222222"
	commitSHA = "abcdef0123456789abcdef0123456789abcdef01"
)

// commitServer serves a repo whose branch main is at testSHA, recording the
// requests in got. Blob n gets the SHA "blob<n>".
func commitServer(t *testing.T, got *[]request) *http.ServeMux {
	t.Helper()
	blobs := 0
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/git/ref/heads/", recorder(t, got, map[string]any{
		"object": map[string]any{"sha": testSHA, "type": "commit"},
	}))
	mux.Handle("GET /repos/o/r/git/commits/", recorder(t, got, map[string]any{
		"sha": testSHA, "tree": map[string]any{"sha": treeSHA},
	}))
	mux.HandleFunc("POST /repos/o/r/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		blobs++
		recorder(t, got, map[string]any{"sha": fmt.Sprint("blob", blobs)})(w, r)
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

func TestGitCommit(t *testing.T) {
	var got []request
	s := fakeGitHub(t, commitServer(t, &got))
	root := withRoot(t, &s)
	writeFiles(t, root, map[string]string{"README.md": "hi\n", "scripts/run.sh": "#!/bin/sh\n"})
	if err := os.Chmod(filepath.Join(root, "scripts", "run.sh"), 0o700); err != nil {
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
		{http.MethodPost, "/repos/o/r/git/blobs", map[string]any{"content": b64("hi\n"), "encoding": "base64"}},
		{http.MethodPost, "/repos/o/r/git/blobs", map[string]any{"content": b64("#!/bin/sh\n"), "encoding": "base64"}},
		{http.MethodPost, "/repos/o/r/git/trees", map[string]any{"base_tree": treeSHA, "tree": []any{
			map[string]any{"path": "README.md", "mode": "100644", "type": "blob", "sha": "blob1"},
			map[string]any{"path": "scripts/run.sh", "mode": "100755", "type": "blob", "sha": "blob2"},
		}}},
		{http.MethodPost, "/repos/o/r/git/commits", map[string]any{
			"message": "Add things\n\nMore detail.\nSecond line.\n", "tree": newTree, "parents": []any{testSHA},
		}},
		{http.MethodPatch, "/repos/o/r/git/refs/heads/feat/x", map[string]any{"sha": commitSHA, "force": false}},
	})
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
		{[]string{"--branch", "main", "-m", "x"}, "requires at least 1 arg"},
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
