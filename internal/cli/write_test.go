package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// request is a write request the fake GitHub received.
type request struct {
	Method, Path string
	Body         map[string]any
}

// recorder handles a pattern by recording the request and replying with v.
func recorder(t *testing.T, got *[]request, v any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		req := request{Method: r.Method, Path: r.URL.Path}
		if r.Body != nil && r.Header.Get("Content-Type") == "application/json" {
			if err := json.NewDecoder(r.Body).Decode(&req.Body); err != nil {
				t.Errorf("%s %s: decode body: %v", r.Method, r.URL.Path, err)
			}
		}
		*got = append(*got, req)
		reply(v)(w, r)
	}
}

// withRoot sets GHZT_ROOT to a new temp dir and returns it.
func withRoot(t *testing.T, s *IO) string {
	t.Helper()
	root := t.TempDir()
	getenv := s.Getenv
	s.Getenv = func(k string) string {
		if k == "GHZT_ROOT" {
			return root
		}
		return getenv(k)
	}
	return root
}

func checkRequests(t *testing.T, got, want []request) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requests:\n got %+v\nwant %+v", got, want)
	}
}

var issueURL = map[string]any{"number": 3, "html_url": "https://github.com/o/r/issues/3"}

func TestIssueCreate(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/issues", recorder(t, &got, issueURL))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "gh", "issue", "create", "-R", "o/r", "-t", "Bug", "-b", "it broke",
		"-l", "bug,p1", "-a", "minta")
	if code != exitOK || out != "https://github.com/o/r/issues/3\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	checkRequests(t, got, []request{{http.MethodPost, "/repos/o/r/issues", map[string]any{
		"title": "Bug", "body": "it broke", "labels": []any{"bug", "p1"}, "assignees": []any{"minta"},
	}}})

	got = nil
	code, _, errOut = runWith(s, "gh", "issue", "create", "-R", "o/r", "-b", "no title")
	if code != 1 || !strings.Contains(errOut, "--title required") || len(got) != 0 {
		t.Errorf("no title: code %d, stderr %q, %d requests", code, errOut, len(got))
	}
}

func TestBodyFile(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/issues/3/comments", recorder(t, &got,
		map[string]any{"id": 1, "html_url": "https://github.com/o/r/issues/3#issuecomment-1"}))
	s := fakeGitHub(t, mux)
	root := withRoot(t, &s)
	if err := os.WriteFile(filepath.Join(root, "body.md"), []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runWith(s, "gh", "issue", "comment", "3", "-R", "o/r", "-F", "body.md")
	if code != exitOK || out != "https://github.com/o/r/issues/3#issuecomment-1\n" || errOut != "" {
		t.Fatalf("file: got (%d, %q, %q)", code, out, errOut)
	}
	s.In = strings.NewReader("from stdin")
	if code, _, errOut := runWith(s, "gh", "pr", "comment", "3", "-R", "o/r", "--body-file", "-"); code != exitOK {
		t.Fatalf("stdin: code %d, stderr %q", code, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodPost, "/repos/o/r/issues/3/comments", map[string]any{"body": "from a file\n"}},
		{http.MethodPost, "/repos/o/r/issues/3/comments", map[string]any{"body": "from stdin"}},
	})

	got = nil
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-F", "../outside.md"}, "outside.md"},
		{[]string{"-b", "x", "-F", "body.md"}, "none of the others can be"},
		{nil, "--body or --body-file required"},
		{[]string{"-b", ""}, "body cannot be blank"},
		{[]string{"-b", strings.Repeat("é", maxBodyChars+1)}, "body is too long: 65537 characters"},
	} {
		args := append([]string{"gh", "issue", "comment", "3", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("bad bodies sent %d requests", len(got))
	}
}

func TestIssueClose(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/issues/3/comments", recorder(t, &got, map[string]any{"id": 1}))
	mux.Handle("PATCH /repos/o/r/issues/3", recorder(t, &got, issueURL))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "gh", "issue", "close", "3", "-R", "o/r", "-r", "not planned", "-c", "won't do")
	if code != exitOK || out != "https://github.com/o/r/issues/3\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if code, _, errOut := runWith(s, "gh", "issue", "close", "3", "-R", "o/r"); code != exitOK {
		t.Fatalf("no reason: code %d, stderr %q", code, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodPost, "/repos/o/r/issues/3/comments", map[string]any{"body": "won't do"}},
		{http.MethodPatch, "/repos/o/r/issues/3", map[string]any{"state": "closed", "state_reason": "not_planned"}},
		{http.MethodPatch, "/repos/o/r/issues/3", map[string]any{"state": "closed"}},
	})

	code, _, errOut = runWith(s, "gh", "issue", "close", "3", "-R", "o/r", "-r", "wontfix")
	if code != 1 || !strings.Contains(errOut, "not planned") {
		t.Errorf("bad reason: code %d, stderr %q", code, errOut)
	}
}

// labeledIssue is an issue with labels and assignees, for the edit tests.
var labeledIssue = map[string]any{
	"number": 3, "html_url": "https://github.com/o/r/issues/3",
	"labels":    []any{map[string]any{"name": "bug"}, map[string]any{"name": "p1"}},
	"assignees": []any{map[string]any{"login": "minta"}},
}

func TestIssueEdit(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/issues/3", reply(labeledIssue))
	mux.Handle("PATCH /repos/o/r/issues/3", recorder(t, &got, issueURL))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "gh", "issue", "edit", "3", "-R", "o/r", "-t", "New title",
		"--add-label", "p2,bug", "--remove-label", "p1")
	if code != exitOK || out != "https://github.com/o/r/issues/3\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if code, _, errOut := runWith(s, "gh", "issue", "edit", "3", "-R", "o/r",
		"--add-assignee", "octocat", "--remove-assignee", "minta"); code != exitOK {
		t.Fatalf("assignees: code %d, stderr %q", code, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodPatch, "/repos/o/r/issues/3", map[string]any{"title": "New title", "labels": []any{"bug", "p2"}}},
		{http.MethodPatch, "/repos/o/r/issues/3", map[string]any{"assignees": []any{"octocat"}}},
	})

	got = nil
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "field to edit flag required"},
		{[]string{"-t", ""}, "title cannot be blank"},
	} {
		args := append([]string{"gh", "issue", "edit", "3", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("bad edits sent %d requests", len(got))
	}
}

func TestSetEditsApply(t *testing.T) {
	e := setEdits{add: []string{"c", "a", "c"}, remove: []string{"b", "x"}}
	if got, want := e.apply([]string{"a", "b"}), []string{"a", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("apply = %q, want %q", got, want)
	}
	e = setEdits{remove: []string{"a"}}
	if got := e.apply([]string{"a"}); got == nil || len(got) != 0 {
		t.Errorf("removing the last one = %#v, want an empty, non-nil slice", got)
	}
}

var prURL = map[string]any{"number": 7, "html_url": "https://github.com/o/r/pull/7"}

func TestPRCreate(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r", reply(map[string]any{"full_name": "o/r", "default_branch": "trunk"}))
	mux.Handle("POST /repos/o/r/pulls", recorder(t, &got, prURL))
	mux.Handle("PATCH /repos/o/r/issues/7", recorder(t, &got, prURL))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "gh", "pr", "create", "-R", "o/r", "-t", "Add x", "-H", "feat/x", "-d", "-l", "enhancement")
	if code != exitOK || out != "https://github.com/o/r/pull/7\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if code, _, errOut := runWith(s, "gh", "pr", "create", "-R", "o/r", "-t", "Fork", "-H", "fork:fix", "-B", "release", "-b", "why"); code != exitOK {
		t.Fatalf("fork: code %d, stderr %q", code, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodPost, "/repos/o/r/pulls", map[string]any{"title": "Add x", "head": "feat/x", "base": "trunk", "draft": true}},
		{http.MethodPatch, "/repos/o/r/issues/7", map[string]any{"labels": []any{"enhancement"}}},
		{http.MethodPost, "/repos/o/r/pulls", map[string]any{"title": "Fork", "head": "fork:fix", "base": "release", "draft": false, "body": "why"}},
	})

	got = nil
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-H", "x"}, "--title required"},
		{[]string{"-t", "T"}, "--head required"},
		{[]string{"-t", "T", "-H", "a:b:c"}, "invalid"},
		{[]string{"-t", "T", "-H", "x", "-B", "a b"}, "invalid branch name"},
	} {
		args := append([]string{"gh", "pr", "create", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("bad creates sent %d requests", len(got))
	}
}

func TestPREdit(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/issues/7", reply(labeledIssue))
	mux.Handle("PATCH /repos/o/r/pulls/7", recorder(t, &got, prURL))
	mux.Handle("PATCH /repos/o/r/issues/7", recorder(t, &got, prURL))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "gh", "pr", "edit", "7", "-R", "o/r", "-B", "release", "--remove-label", "bug")
	if code != exitOK || out != "https://github.com/o/r/pull/7\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if code, _, errOut := runWith(s, "gh", "pr", "edit", "7", "-R", "o/r", "-t", "Only title"); code != exitOK {
		t.Fatalf("title: code %d, stderr %q", code, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodPatch, "/repos/o/r/pulls/7", map[string]any{"base": "release"}},
		{http.MethodPatch, "/repos/o/r/issues/7", map[string]any{"labels": []any{"p1"}}},
		{http.MethodPatch, "/repos/o/r/pulls/7", map[string]any{"title": "Only title"}},
	})

	if code, _, errOut := runWith(s, "gh", "pr", "edit", "7", "-R", "o/r"); code != 1 || !strings.Contains(errOut, "field to edit") {
		t.Errorf("nothing to edit: code %d, stderr %q", code, errOut)
	}
}

func TestLabelCreate(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/labels", recorder(t, &got, map[string]any{"name": "good first issue"}))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "gh", "label", "create", "good first issue", "-R", "o/r", "-c", "#7057FF", "-d", "Easy")
	if code != exitOK || out != "https://github.com/o/r/labels/good%20first%20issue\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if code, _, errOut := runWith(s, "gh", "label", "create", "plain", "-R", "o/r"); code != exitOK {
		t.Fatalf("no color: code %d, stderr %q", code, errOut)
	}
	checkRequests(t, got, []request{
		{http.MethodPost, "/repos/o/r/labels", map[string]any{"name": "good first issue", "color": "7057FF", "description": "Easy"}},
		{http.MethodPost, "/repos/o/r/labels", map[string]any{"name": "plain"}},
	})

	code, _, errOut = runWith(s, "gh", "label", "create", "x", "-R", "o/r", "-c", "red")
	if code != 1 || !strings.Contains(errOut, `invalid color "red"`) {
		t.Errorf("bad color: code %d, stderr %q", code, errOut)
	}
}

func TestCommitComment(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/commits/abc1234/comments", recorder(t, &got,
		map[string]any{"id": 5, "html_url": "https://github.com/o/r/commit/abc1234#commitcomment-5"}))
	s := fakeGitHub(t, mux)

	code, out, errOut := runWith(s, "ghzt", "commit-comment", "abc1234", "-R", "o/r", "-b", "nice", "--path", "a/b.go", "--position", "3")
	if code != exitOK || out != "https://github.com/o/r/commit/abc1234#commitcomment-5\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	checkRequests(t, got, []request{{
		http.MethodPost, "/repos/o/r/commits/abc1234/comments",
		map[string]any{"body": "nice", "path": "a/b.go", "position": float64(3)},
	}})

	got = nil
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"main", "-b", "x"}, "invalid commit SHA"},
		{[]string{"abc1234", "-b", "x", "--position", "1"}, "--position requires --path"},
		{[]string{"abc1234", "-b", "x", "--path", "../x"}, "invalid path"},
		{[]string{"abc1234"}, "--body or --body-file required"},
	} {
		args := append([]string{"ghzt", "commit-comment", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != 1 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("bad comments sent %d requests", len(got))
	}
}

func TestWriteAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/o/r/issues", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"code":"invalid","field":"assignees"}]}`))
	})
	s := fakeGitHub(t, mux)
	code, out, errOut := runWith(s, "gh", "issue", "create", "-R", "o/r", "-t", "x", "-a", "nobody")
	if code != 1 || out != "" || !strings.Contains(errOut, "POST /repos/o/r/issues") || !strings.Contains(errOut, "422") {
		t.Errorf("got (%d, %q, %q)", code, out, errOut)
	}
}
