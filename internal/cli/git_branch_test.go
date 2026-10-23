package cli

import (
	"net/http"
	"strings"
	"testing"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

// shaOf returns a handler serving sha the way the commits API serves a commit
// asked for with the sha media type, and records the commit asked for.
func shaOf(t *testing.T, sha string, got *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Accept"); a != "application/vnd.github.sha" {
			t.Errorf("Accept = %q", a)
		}
		*got = append(*got, r.URL.Path)
		w.Header().Set("Content-Type", "application/vnd.github.sha; charset=utf-8")
		_, _ = w.Write([]byte(sha))
	}
}

func TestGitBranch(t *testing.T) {
	var resolved []string
	var created []request
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/commits/", shaOf(t, testSHA, &resolved))
	mux.Handle("POST /repos/o/r/git/refs", recorder(t, &created, map[string]any{"ref": "refs/heads/x"}))
	s := fakeGitHub(t, mux)
	for _, tc := range []struct {
		args          []string
		commit, refBy string
		url           string
	}{
		{[]string{"feat/x"}, "/repos/o/r/commits/HEAD", "refs/heads/feat/x", "https://github.com/o/r/tree/feat/x"},
		{[]string{"fix", "release/v1"}, "/repos/o/r/commits/release/v1", "refs/heads/fix", "https://github.com/o/r/tree/fix"},
		{[]string{"a#b", testSHA}, "/repos/o/r/commits/" + testSHA, "refs/heads/a#b", "https://github.com/o/r/tree/a%23b"},
	} {
		resolved, created = nil, nil
		code, out, errOut := runWith(s, append([]string{"git", "branch", "-R", "o/r"}, tc.args...)...)
		if code != exitOK || out != tc.url+"\n" || errOut != "" {
			t.Errorf("%q: got (%d, %q, %q)", tc.args, code, out, errOut)
			continue
		}
		if len(resolved) != 1 || resolved[0] != tc.commit {
			t.Errorf("%q: resolved %q, want %q", tc.args, resolved, tc.commit)
		}
		checkRequests(t, created, []request{{http.MethodPost, "/repos/o/r/git/refs", map[string]any{
			"ref": tc.refBy, "sha": testSHA,
		}}})
	}
}

func TestGitBranchInvalid(t *testing.T) {
	var resolved []string
	var created []request
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/commits/", shaOf(t, testSHA, &resolved))
	mux.Handle("POST /repos/o/r/git/refs", recorder(t, &created, nil))
	s := fakeGitHub(t, mux)
	for _, args := range [][]string{
		{"HEAD"},
		{"@"},
		{"-x"},
		{"a..b"},
		{"a/"},
		{"a//b"},
		{".a"},
		{"a/.b"},
		{"a.lock"},
		{"a.lock/b"},
		{"a."},
		{"a b"},
		{"a~1"},
		{"a:b"},
		{"a@{1}"},
		{"a\\b"},
		{"ok", "main~1"},
		{"ok", "../x"},
		{"ok", "a//b"},
		{"ok", "main", "extra"},
	} {
		code, _, errOut := runWith(s, append([]string{"git", "branch", "-R", "o/r", "--"}, args...)...)
		if code == exitOK || errOut == "" {
			t.Errorf("%q: code %d, stderr %q", args, code, errOut)
		}
	}
	if len(resolved) != 0 || len(created) != 0 {
		t.Errorf("made requests: %q, %+v", resolved, created)
	}
	for _, name := range []string{"a.b", "v1.2/x.y", "a-lock", "x.lockfile", "@a", "a@b"} {
		if err := checkNewBranch(name); err != nil {
			t.Errorf("checkNewBranch(%q) = %v", name, err)
		}
	}
}

func TestGitBranchErrors(t *testing.T) {
	var resolved []string
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/commits/main", shaOf(t, testSHA, &resolved))
	mux.Handle("GET /repos/o/r/commits/odd", shaOf(t, "<html>", &resolved))
	mux.HandleFunc("POST /repos/o/r/git/refs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
	})
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "git", "branch", "-R", "o/r", "dup", "main")
	if code != 1 || out != "" || !strings.Contains(errOut, "Reference already exists") {
		t.Errorf("existing branch: got (%d, %q, %q)", code, out, errOut)
	}
	code, out, errOut = runWith(s, "git", "branch", "-R", "o/r", "x", "odd")
	if code != 1 || out != "" || !strings.Contains(errOut, `unexpected response resolving odd: "<html>"`) {
		t.Errorf("not a SHA: got (%d, %q, %q)", code, out, errOut)
	}
	code, out, errOut = runWith(s, "git", "branch", "-R", "o/r", "x", "missing")
	if code != 1 || out != "" || !strings.Contains(errOut, "404") {
		t.Errorf("missing start point: got (%d, %q, %q)", code, out, errOut)
	}
}
