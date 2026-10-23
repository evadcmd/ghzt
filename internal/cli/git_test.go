package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// rawFile returns a handler serving data the way the contents API serves a
// file asked for with the raw media type.
func rawFile(data string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.github.raw+json; charset=utf-8")
		_, _ = w.Write([]byte(data))
	}
}

func TestGitShowFile(t *testing.T) {
	mux := http.NewServeMux()
	var gotRef string
	mux.HandleFunc("GET /repos/o/r/contents/docs/a.md", func(w http.ResponseWriter, r *http.Request) {
		gotRef = r.URL.Query().Get("ref")
		if got := r.Header.Get("Accept"); got != "application/vnd.github.raw+json" {
			t.Errorf("Accept = %q", got)
		}
		rawFile("# A\n")(w, r)
	})
	s := fakeGitHub(t, mux)
	for _, tc := range []struct{ object, ref string }{
		{"feature/x:docs/a.md", "feature/x"},
		{":docs/a.md", ""},
		{"HEAD:docs/a.md", ""},
	} {
		code, out, errOut := runWith(s, "git", "show", "-R", "o/r", tc.object)
		if code != 0 || out != "# A\n" || errOut != "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", tc.object, code, out, errOut)
		}
		if gotRef != tc.ref {
			t.Errorf("%s: ref = %q, want %q", tc.object, gotRef, tc.ref)
		}
	}
}

func TestGitShowDirectory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/contents/docs", reply([]any{
		map[string]any{"name": "a.md", "type": "file"},
		map[string]any{"name": "img", "type": "dir"},
	}))
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "main:docs/")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if want := "tree main:docs/\n\na.md\nimg/\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	code, _, errOut = runWith(s, "git", "show", "-R", "o/r", "main:docs", "-o", "x")
	if code != 1 || !strings.Contains(errOut, "main:docs is a directory") {
		t.Errorf("-o on a directory: code %d, stderr %q", code, errOut)
	}
}

func TestGitShowFileOutput(t *testing.T) {
	data := "bin\x00ary" + strings.Repeat("x", maxShowBytes)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/contents/logo.png", rawFile(data))
	s := fakeGitHub(t, mux)
	root := t.TempDir()
	getenv := s.Getenv
	s.Getenv = func(k string) string {
		if k == "GHZT_ROOT" {
			return root
		}
		return getenv(k)
	}

	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "main:logo.png")
	if code != 1 || out != "" || !strings.Contains(errOut, "binary file") || !strings.Contains(errOut, "-o") {
		t.Errorf("binary to stdout: code %d, stdout %q, stderr %q", code, out, errOut)
	}

	code, out, errOut = runWith(s, "git", "show", "-R", "o/r", "main:logo.png", "-o", "out/logo.png")
	if code != 0 || out != "" || !strings.Contains(errOut, "wrote 100007 bytes to out/logo.png") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	got, err := os.ReadFile(filepath.Join(root, "out", "logo.png")) //nolint:gosec // G304: a path under the test temp dir.
	if err != nil || string(got) != data {
		t.Errorf("file = %d bytes, %v; want %d bytes", len(got), err, len(data))
	}

	code, _, errOut = runWith(s, "git", "show", "-R", "o/r", "main:logo.png", "-o", "../logo.png")
	if code != 1 || !strings.Contains(errOut, "outside") {
		t.Errorf("-o outside the root: code %d, stderr %q", code, errOut)
	}
}

func TestGitShowFileTruncated(t *testing.T) {
	// A multibyte character straddles the cut, which must not split it.
	data := strings.Repeat("a", maxShowBytes-1) + "é" + "tail"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/contents/big.txt", rawFile(data))
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "main:big.txt")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if out != data[:maxShowBytes-1] {
		t.Errorf("stdout is %d bytes, want %d", len(out), maxShowBytes-1)
	}
	if want := "… truncated, 6 more bytes; save all of it with -o\n"; errOut != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
}

func TestGitShowFileTruncatedNotUTF8(t *testing.T) {
	// Latin-1 text has no NUL and no rune starts: the cut backs off at most
	// one rune's worth instead of to the start.
	data := "ok\n" + strings.Repeat("\xa9", 2*maxShowBytes)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/contents/latin1.txt", rawFile(data))
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "main:latin1.txt")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if n := maxShowBytes - (utf8.UTFMax - 1); out != data[:n] {
		t.Errorf("stdout is %d bytes, want %d", len(out), n)
	}
	if want := "… truncated, 100006 more bytes; save all of it with -o\n"; errOut != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
}

var testCommit = map[string]any{
	"sha": "1111111111111111111111111111111111111111",
	"commit": map[string]any{
		"author":  map[string]any{"name": "Minta", "email": "m@example.com", "date": "2026-10-22T09:30:00Z"},
		"message": "Add the thing\n\nIt does stuff.\n",
	},
	"parents": []any{map[string]any{"sha": "2222222222222222222222222222222222222222"}},
	"files": []any{
		map[string]any{"filename": "a.go", "status": "modified", "additions": 1, "deletions": 1, "patch": "@@ -1 +1 @@\n-old\n+new"},
		map[string]any{"filename": "docs/new.md", "status": "added", "additions": 2, "deletions": 0, "patch": "@@ -0,0 +1,2 @@\n+x\n+y"},
		map[string]any{"filename": "lib/b.go", "previous_filename": "b.go", "status": "renamed", "additions": 0, "deletions": 0},
		map[string]any{"filename": "logo.png", "status": "added", "additions": 0, "deletions": 0},
	},
}

func TestGitShowCommit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/commits/feature/x", reply(testCommit))
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "feature/x")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	want := `commit 1111111111111111111111111111111111111111
Author: Minta <m@example.com>
Date:   Thu Oct 22 09:30:00 2026 +0000

    Add the thing

    It does stuff.

diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
-old
+new
diff --git a/docs/new.md b/docs/new.md
--- /dev/null
+++ b/docs/new.md
@@ -0,0 +1,2 @@
+x
+y
diff --git a/b.go b/lib/b.go
rename from b.go
rename to lib/b.go
diff --git a/logo.png b/logo.png
(no patch from the API: binary file or diff too large)
`
	if out != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, want)
	}
}

func TestGitShowCommitStatAndPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/commits/HEAD", reply(testCommit))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "--stat")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	wantStat := `commit 1111111111111111111111111111111111111111
Author: Minta <m@example.com>
Date:   Thu Oct 22 09:30:00 2026 +0000

    Add the thing

    It does stuff.

 a.go             | 2 +-
 docs/new.md      | 2 ++
 b.go => lib/b.go | 0
 logo.png         | 0
 4 files changed, 3 insertions(+), 1 deletion(-)
`
	if out != wantStat {
		t.Errorf("--stat stdout:\n%s\nwant:\n%s", out, wantStat)
	}

	code, out, errOut = runWith(s, "git", "show", "-R", "o/r", "HEAD", "--", "docs/", "b.go")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if strings.Contains(out, "a/a.go") || strings.Contains(out, "logo.png") ||
		!strings.Contains(out, "b/docs/new.md") || !strings.Contains(out, "rename to lib/b.go") {
		t.Errorf("pathspec stdout:\n%s", out)
	}
}

func TestGitShowCommitPages(t *testing.T) {
	mux := http.NewServeMux()
	sha := "1111111111111111111111111111111111111111"
	mux.HandleFunc("GET /repos/o/r/commits/main", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://api.github.com/x?page=2>; rel="next"`)
		reply(map[string]any{"sha": sha, "files": []any{map[string]any{"filename": "p1", "additions": 1}}})(w, r)
	})
	mux.HandleFunc("GET /repos/o/r/commits/"+sha, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("page = %q", got)
		}
		reply(map[string]any{"sha": sha, "files": []any{map[string]any{"filename": "p2", "deletions": 1}}})(w, r)
	})
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "git", "show", "-R", "o/r", "main", "--stat")
	if code != 0 || !strings.Contains(out, " p1 | 1 +\n p2 | 1 -\n 2 files changed, 1 insertion(+), 1 deletion(-)\n") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestGitShowMerge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/commits/abc", reply(map[string]any{
		"sha":     "abc",
		"commit":  map[string]any{"author": map[string]any{"name": "M", "email": "m@x", "date": "2026-10-22T09:30:00+09:00"}, "message": "Merge"},
		"parents": []any{map[string]any{"sha": "aaaaaaaaaa"}, map[string]any{"sha": "bbbbbbbbbb"}},
	}))
	s := fakeGitHub(t, mux)
	code, out, _ := runWith(s, "git", "show", "-R", "o/r", "abc")
	want := "commit abc\nMerge: aaaaaaa bbbbbbb\nAuthor: M <m@x>\nDate:   Thu Oct 22 09:30:00 2026 +0900\n\n    Merge\n"
	if code != 0 || out != want {
		t.Errorf("code %d, stdout %q, want %q", code, out, want)
	}
}

func TestGitShowArgErrors(t *testing.T) {
	s := fakeGitHub(t, http.NewServeMux())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"a", "b"}, "expected one commit"},
		{[]string{"main:a.go", "--stat"}, "only apply to a commit"},
		{[]string{"main:a.go", "--", "x"}, "only apply to a commit"},
		{[]string{"main", "-o", "x"}, "-o only applies"},
		{[]string{"main:../a.go"}, "invalid path"},
		{[]string{"main:"}, "invalid path"},
		{[]string{"HEAD~1"}, "invalid ref"},
		{[]string{"a..b"}, "invalid ref"},
		{[]string{"feature//x"}, "invalid ref"},
		{[]string{"."}, "invalid ref"},
		{[]string{"a/./b"}, "invalid ref"},
		{[]string{"x/."}, "invalid ref"},
		{[]string{":"}, "invalid path"},
		{[]string{"main", "--", "../x"}, "invalid path"},
	} {
		args := append([]string{"git", "show", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
}

var testTree = map[string]any{
	"sha": "t1",
	"tree": []any{
		map[string]any{"path": "README.md", "mode": "100644", "type": "blob", "sha": "b1"},
		map[string]any{"path": "docs", "mode": "040000", "type": "tree", "sha": "t2"},
		map[string]any{"path": "docs/a.md", "mode": "100644", "type": "blob", "sha": "b2"},
		map[string]any{"path": "docs/run.sh", "mode": "100755", "type": "blob", "sha": "b3"},
	},
	"truncated": false,
}

func TestGitLsTree(t *testing.T) {
	mux := http.NewServeMux()
	var recursive string
	mux.HandleFunc("GET /repos/o/r/git/trees/feature/x", func(w http.ResponseWriter, r *http.Request) {
		recursive = r.URL.Query().Get("recursive")
		reply(testTree)(w, r)
	})
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "git", "ls-tree", "-R", "o/r", "-r", "--name-only", "feature/x")
	if code != 0 || errOut != "" || recursive != "1" {
		t.Fatalf("code %d, stderr %q, recursive %q", code, errOut, recursive)
	}
	if want := "README.md\ndocs/a.md\ndocs/run.sh\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}

	code, out, _ = runWith(s, "git", "ls-tree", "-R", "o/r", "-r", "feature/x", "docs/run.sh")
	if want := "100755 blob b3\tdocs/run.sh\n"; code != 0 || out != want {
		t.Errorf("code %d, stdout %q, want %q", code, out, want)
	}

	code, out, _ = runWith(s, "git", "ls-tree", "-R", "o/r", "feature/x")
	if code != 0 || recursive != "" || !strings.Contains(out, "040000 tree t2\tdocs\n") {
		t.Errorf("code %d, recursive %q, stdout %q", code, recursive, out)
	}
}

func TestGitLsTreeTruncated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/git/trees/main", reply(map[string]any{
		"tree": []any{map[string]any{"path": "a\nb", "mode": "100644", "type": "blob", "sha": "b1"}}, "truncated": true,
	}))
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "git", "ls-tree", "-R", "o/r", "-r", "--name-only", "main")
	if code != 0 || out != "\"a\\nb\"\n" || !strings.Contains(errOut, "list subtrees by their SHA") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestGitLsTreeArgErrors(t *testing.T) {
	s := fakeGitHub(t, http.NewServeMux())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"main", "docs"}, "paths need -r"},
		{[]string{"-r", "main", "../x"}, "invalid path"},
		{[]string{"x..y"}, "invalid ref"},
		{[]string{"."}, "invalid ref"},
		{[]string{"./x"}, "invalid ref"},
		{nil, "requires at least 1 arg"},
	} {
		args := append([]string{"git", "ls-tree", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
}
