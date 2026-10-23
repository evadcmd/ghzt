package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var reviewURL = map[string]any{"id": 80, "html_url": "https://github.com/o/r/pull/3#pullrequestreview-80"}

func TestPRReview(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/pulls/3/reviews", recorder(t, &got, reviewURL))
	s := fakeGitHub(t, mux)
	root := withRoot(t, &s)
	comments := `[{"path": "a/b.go", "line": 7, "body": "why?"}, {"path": "c.go", "line": 2, "side": "LEFT", "body": "gone"}]`
	if err := os.WriteFile(filepath.Join(root, "c.json"), []byte(comments), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		args []string
		want map[string]any
	}{
		{[]string{"--approve"}, map[string]any{"event": "APPROVE"}},
		{[]string{"-a", "-b", "lgtm"}, map[string]any{"event": "APPROVE", "body": "lgtm"}},
		{[]string{"-r", "-b", "no"}, map[string]any{"event": "REQUEST_CHANGES", "body": "no"}},
		{[]string{"-c", "--comments-file", "c.json"}, map[string]any{"event": "COMMENT", "comments": []any{
			map[string]any{"path": "a/b.go", "line": float64(7), "body": "why?"},
			map[string]any{"path": "c.go", "line": float64(2), "side": "LEFT", "body": "gone"},
		}}},
	} {
		got = nil
		args := append([]string{"gh", "pr", "review", "3", "-R", "o/r"}, tc.args...)
		code, out, errOut := runWith(s, args...)
		if code != exitOK || out != "https://github.com/o/r/pull/3#pullrequestreview-80\n" || errOut != "" {
			t.Fatalf("%q: got (%d, %q, %q)", tc.args, code, out, errOut)
		}
		checkRequests(t, got, []request{{http.MethodPost, "/repos/o/r/pulls/3/reviews", tc.want}})
	}

	got = nil
	s.In = strings.NewReader(comments)
	code, _, errOut := runWith(s, "gh", "pr", "review", "3", "-R", "o/r", "-c", "-b", "see inline", "--comments-file", "-")
	if code != exitOK || len(got) != 1 || got[0].Body["body"] != "see inline" || len(got[0].Body["comments"].([]any)) != 2 {
		t.Errorf("comments from stdin: code %d, stderr %q, requests %+v", code, errOut, got)
	}
}

func TestPRReviewInvalid(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	mux.Handle("POST /repos/o/r/pulls/3/reviews", recorder(t, &got, reviewURL))
	s := fakeGitHub(t, mux)
	root := withRoot(t, &s)
	for name, content := range map[string]string{
		"path.json":     `[{"path": "../x", "line": 1, "body": "x"}]`,
		"line.json":     `[{"path": "a.go", "line": 0, "body": "x"}]`,
		"side.json":     `[{"path": "a.go", "line": 1, "side": "right", "body": "x"}]`,
		"body.json":     `[{"path": "a.go", "line": 1, "body": ""}]`,
		"unknown.json":  `[{"path": "a.go", "position": 1, "body": "x"}]`,
		"trailing.json": `[] []`,
		"bracket.json":  `[]]`,
		"brace.json":    `[]}`,
		"null.json":     `null`,
		"object.json":   `{"path": "a.go"}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "at least one of the flags in the group [approve comment request-changes] is required"},
		{[]string{"-a", "-c"}, "were all set"},
		{[]string{"-r"}, "body cannot be blank for request-changes review"},
		{[]string{"-c", "-b", ""}, "body cannot be blank for comment review"},
		{[]string{"-c", "--comments-file", "path.json"}, "comment 1: invalid path"},
		{[]string{"-c", "--comments-file", "line.json"}, "comment 1: invalid line 0"},
		{[]string{"-c", "--comments-file", "side.json"}, `invalid side "right"`},
		{[]string{"-c", "--comments-file", "body.json"}, "body cannot be blank"},
		{[]string{"-c", "--comments-file", "unknown.json"}, `unknown field "position"`},
		{[]string{"-c", "--comments-file", "trailing.json"}, "unexpected data after the JSON array"},
		{[]string{"-c", "--comments-file", "bracket.json"}, "unexpected data after the JSON array"},
		{[]string{"-c", "--comments-file", "brace.json"}, "unexpected data after the JSON array"},
		{[]string{"-a", "--comments-file", "null.json"}, "expected a JSON array"},
		{[]string{"-c", "--comments-file", "object.json"}, "cannot unmarshal object"},
		{[]string{"-c", "--comments-file", "missing.json"}, "missing.json"},
		{[]string{"-c", "-F", "-", "--comments-file", "-"}, "can't both read standard input"},
	} {
		args := append([]string{"gh", "pr", "review", "3", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != exitError || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("bad reviews sent %d requests", len(got))
	}
}

// reviewComments serves comment 10, which starts a thread, and 11, a reply
// to it.
func reviewComments(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/o/r/pulls/comments/10", reply(map[string]any{
		"id": 10, "html_url": "https://github.com/o/r/pull/3#discussion_r10",
	}))
	mux.HandleFunc("GET /repos/o/r/pulls/comments/11", reply(map[string]any{
		"id": 11, "in_reply_to_id": 10, "html_url": "https://github.com/o/r/pull/3#discussion_r11",
	}))
}

func TestReviewReply(t *testing.T) {
	var got []request
	mux := http.NewServeMux()
	reviewComments(mux)
	mux.Handle("POST /repos/o/r/pulls/3/comments/10/replies", recorder(t, &got,
		map[string]any{"id": 12, "in_reply_to_id": 10, "html_url": "https://github.com/o/r/pull/3#discussion_r12"}))
	s := fakeGitHub(t, mux)

	for _, id := range []string{"10", "11"} {
		got = nil
		code, out, errOut := runWith(s, "ghzt", "review-reply", "3", id, "-R", "o/r", "-b", "done")
		if code != exitOK || out != "https://github.com/o/r/pull/3#discussion_r12\n" || errOut != "" {
			t.Fatalf("%s: got (%d, %q, %q)", id, code, out, errOut)
		}
		checkRequests(t, got, []request{{http.MethodPost, "/repos/o/r/pulls/3/comments/10/replies", map[string]any{"body": "done"}}})
	}

	got = nil
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"3", "0", "-b", "x"}, `invalid comment ID "0"`},
		{[]string{"3", "r10", "-b", "x"}, `invalid comment ID "r10"`},
		{[]string{"3", "10"}, "--body or --body-file required"},
		{[]string{"3", "99", "-b", "x"}, "HTTP 404"},
	} {
		args := append([]string{"ghzt", "review-reply", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != exitError || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("bad replies sent %d requests", len(got))
	}
}

// graphQLServer answers POST /graphql with pages of review threads, after
// their cursor, and records the threads the mutation resolves.
type graphQLServer struct {
	t        *testing.T
	pages    [][]map[string]any
	resolved []string
	errors   []any
}

func thread(id string, resolved bool, commentID any) map[string]any {
	return map[string]any{"id": id, "isResolved": resolved, "comments": map[string]any{"nodes": []any{
		map[string]any{"fullDatabaseId": commentID, "url": "https://github.com/o/r/pull/3#discussion_r" + id},
	}}}
}

func (g *graphQLServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.t.Errorf("decode GraphQL request: %v", err)
	}
	if g.errors != nil {
		reply(map[string]any{"data": nil, "errors": g.errors})(w, r)
		return
	}
	if strings.HasPrefix(req.Query, "mutation") {
		id, _ := req.Variables["id"].(string)
		g.resolved = append(g.resolved, id)
		reply(map[string]any{"data": map[string]any{"resolveReviewThread": map[string]any{"thread": map[string]any{"isResolved": true}}}})(w, r)
		return
	}
	if req.Variables["owner"] != "o" || req.Variables["name"] != "r" || req.Variables["number"] != float64(3) {
		reply(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": nil}}})(w, r)
		return
	}
	page := 0
	if after, ok := req.Variables["after"].(string); ok {
		page = int(after[0] - '0')
	}
	more := page+1 < len(g.pages)
	reply(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"reviewThreads": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": more, "endCursor": string(rune('0' + page + 1))},
			"nodes":    g.pages[page],
		},
	}}}})(w, r)
}

func TestReviewResolve(t *testing.T) {
	g := &graphQLServer{t: t, pages: [][]map[string]any{
		{thread("A", false, "7"), thread("B", true, 8)},
		{thread("C", false, "10")},
	}}
	mux := http.NewServeMux()
	reviewComments(mux)
	mux.HandleFunc("GET /repos/o/r/pulls/comments/8", reply(map[string]any{"id": 8}))
	mux.Handle("POST /graphql", g)
	s := fakeGitHub(t, mux)

	// 11 is a reply in the thread 10 starts, on the second page.
	code, out, errOut := runWith(s, "ghzt", "review-resolve", "3", "11", "-R", "o/r")
	if code != exitOK || out != "https://github.com/o/r/pull/3#discussion_rC\n" || errOut != "" {
		t.Fatalf("got (%d, %q, %q)", code, out, errOut)
	}
	if len(g.resolved) != 1 || g.resolved[0] != "C" {
		t.Errorf("resolved %q, want [C]", g.resolved)
	}

	// B is resolved already, and its ID comes as a number.
	g.resolved = nil
	code, out, errOut = runWith(s, "ghzt", "review-resolve", "3", "8", "-R", "o/r")
	if code != exitOK || out != "https://github.com/o/r/pull/3#discussion_rB\n" || errOut != "" {
		t.Fatalf("resolved thread: got (%d, %q, %q)", code, out, errOut)
	}
	if len(g.resolved) != 0 {
		t.Errorf("resolved %q again", g.resolved)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"4", "10"}, "pull request o/r#4 not found"},
		{[]string{"3", "99"}, "HTTP 404"},
	} {
		args := append([]string{"ghzt", "review-resolve", "-R", "o/r"}, tc.args...)
		code, _, errOut := runWith(s, args...)
		if code != exitError || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: code %d, stderr %q, want %q", tc.args, code, errOut, tc.want)
		}
	}

	g.pages = [][]map[string]any{{thread("A", false, "7")}}
	code, _, errOut = runWith(s, "ghzt", "review-resolve", "3", "10", "-R", "o/r")
	if code != exitError || !strings.Contains(errOut, "comment 10 is not in a review thread of pull request o/r#3") {
		t.Errorf("no thread: code %d, stderr %q", code, errOut)
	}

	g.errors = []any{map[string]any{"message": "Resource not accessible\nby integration"}}
	code, _, errOut = runWith(s, "ghzt", "review-resolve", "3", "10", "-R", "o/r")
	if code != exitError || !strings.Contains(errOut, "GraphQL: Resource not accessible; by integration (POST /graphql)") {
		t.Errorf("GraphQL error: code %d, stderr %q", code, errOut)
	}
	if len(g.resolved) != 0 {
		t.Errorf("resolved %q after an error", g.resolved)
	}
}
