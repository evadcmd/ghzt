package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeGitHub serves the API from mux and returns the IO that sends the
// commands' requests there, with a token in the environment.
func fakeGitHub(t *testing.T, mux *http.ServeMux) IO {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return IO{
		In: strings.NewReader(""),
		Getenv: func(k string) string {
			if k == "GHZT_TOKEN" {
				return "test-token"
			}
			return ""
		},
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.github.com" {
				return nil, fmt.Errorf("request to unexpected host %q", req.URL.Host)
			}
			if got := req.Header.Get("Authorization"); got != "token test-token" && got != "Bearer test-token" {
				return nil, fmt.Errorf("Authorization = %q", got)
			}
			req = req.Clone(req.Context())
			req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func runWith(s IO, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	s.Out, s.ErrOut = &out, &errOut
	code = Run(args, s)
	return code, out.String(), errOut.String()
}

// reply returns a handler writing v as JSON.
func reply(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

var testIssue = map[string]any{
	"number": 3, "title": "Port the tools", "state": "closed", "state_reason": "completed",
	"body": "the body", "user": map[string]any{"login": "minta"},
	"labels":   []any{map[string]any{"name": "enhancement", "color": "a2eeef"}},
	"comments": 1, "html_url": "https://github.com/o/r/issues/3",
	"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-02T00:00:00Z", "closed_at": "2026-10-03T00:00:00Z",
}

func issueMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/issues/3", reply(testIssue))
	mux.Handle("GET /repos/o/r/issues/3/comments", reply([]any{
		map[string]any{"id": 11, "user": nil, "body": "line1\nline2", "created_at": "2026-10-01T01:00:00Z"},
	}))
	return mux
}

func TestIssueView(t *testing.T) {
	s := fakeGitHub(t, issueMux())
	code, stdout, stderr := runWith(s, "gh", "issue", "view", "#3", "-R", "o/r")
	want := "title:\tPort the tools\nstate:\tCLOSED (COMPLETED)\nauthor:\tminta\nlabels:\tenhancement\n" +
		"comments:\t1\nnumber:\t3\nurl:\thttps://github.com/o/r/issues/3\n--\nthe body\n"
	if code != exitOK || stdout != want || stderr != "" {
		t.Errorf("got (%d, %q, %q), want (0, %q, \"\")", code, stdout, stderr, want)
	}

	code, stdout, _ = runWith(s, "gh", "issue", "view", "3", "-R", "o/r", "--comments")
	want = "author:\tghost\nid:\t11\ncreated:\t2026-10-01T01:00:00Z\n--\nline1\nline2\n--\n"
	if code != exitOK || stdout != want {
		t.Errorf("--comments: got (%d, %q), want (0, %q)", code, stdout, want)
	}
}

func TestIssueViewJSON(t *testing.T) {
	s := fakeGitHub(t, issueMux())
	code, stdout, stderr := runWith(s, "gh", "issue", "view", "3", "-R", "o/r", "--json", "number,state,closedAt,comments")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := decode[map[string]any](t, stdout)
	if len(got) != 4 || got["number"] != 3.0 || got["state"] != "CLOSED" || got["closedAt"] != "2026-10-03T00:00:00Z" {
		t.Errorf("got %v", got)
	}
	if cs, _ := got["comments"].([]any); len(cs) != 1 {
		t.Errorf("comments = %v, want one", got["comments"])
	}
}

func TestJSONFlagErrors(t *testing.T) {
	s := fakeGitHub(t, issueMux())
	code, stdout, stderr := runWith(s, "gh", "issue", "view", "3", "-R", "o/r", "--json", "number,bogus")
	if code != exitError || stdout != "" || !strings.Contains(stderr, `unknown JSON field: "bogus"`) ||
		!strings.Contains(stderr, "\n  stateReason\n") {
		t.Errorf("unknown field: got (%d, %q, %q)", code, stdout, stderr)
	}
	code, _, stderr = runWith(s, "gh", "issue", "view", "3", "-R", "o/r", "--json")
	if code != exitError || !strings.Contains(stderr, "specify one or more comma-separated fields for --json:\n  author\n") {
		t.Errorf("bare --json: got (%d, %q)", code, stderr)
	}
}

func TestRepoFlag(t *testing.T) {
	s := fakeGitHub(t, issueMux())
	for _, repo := range []string{"o", "o/r/x", "../r", "o/..", "-o/r", "o/r?x"} {
		code, _, stderr := runWith(s, "gh", "issue", "view", "3", "-R", repo)
		if code != exitError || !strings.Contains(stderr, "invalid argument") {
			t.Errorf("-R %q: got (%d, %q), want a flag error", repo, code, stderr)
		}
	}
	code, _, stderr := runWith(s, "gh", "issue", "view", "3")
	if code != exitError || !strings.Contains(stderr, `required flag(s) "repo" not set`) {
		t.Errorf("no -R: got (%d, %q)", code, stderr)
	}
}

func TestAPIErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues/404", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("GET /repos/o/r/issues/403", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1792800000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for installation"}`))
	})
	s := fakeGitHub(t, mux)
	tests := []struct{ number, want string }{
		{"404", "gh: HTTP 404: Not Found (GET /repos/o/r/issues/404)\n"},
		{"403", "gh: HTTP 403: API rate limit exceeded, resets at 2026-10-24T00:00:00Z (GET /repos/o/r/issues/403)\n"},
	}
	for _, tt := range tests {
		code, stdout, stderr := runWith(s, "gh", "issue", "view", tt.number, "-R", "o/r")
		if code != exitError || stdout != "" || stderr != tt.want {
			t.Errorf("%s: got (%d, %q, %q), want stderr %q", tt.number, code, stdout, stderr, tt.want)
		}
	}
}

func TestNoCredentials(t *testing.T) {
	s := fakeGitHub(t, issueMux())
	s.Getenv = func(string) string { return "" }
	code, _, stderr := runWith(s, "gh", "issue", "view", "3", "-R", "o/r")
	if code != exitError || !strings.HasPrefix(stderr, "gh: no GitHub credentials: set GHZT_TOKEN") {
		t.Errorf("got (%d, %q)", code, stderr)
	}
}

func pull(number int, head, state, mergedAt string) map[string]any {
	p := map[string]any{
		"number": number, "title": fmt.Sprintf("PR %d", number), "state": state,
		"head": map[string]any{"ref": head}, "base": map[string]any{"ref": "main"},
		"user": map[string]any{"login": "minta"}, "created_at": "2026-10-01T00:00:00Z",
	}
	if mergedAt != "" {
		p["merged_at"] = mergedAt
	}
	return p
}

func TestPRList(t *testing.T) {
	var queries []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://api.github.com/repos/o/r/pulls?page=2>; rel="next"`)
			reply([]any{pull(5, "a", "closed", ""), pull(4, "b", "closed", "2026-10-02T00:00:00Z")})(w, r)
			return
		}
		reply([]any{pull(3, "c", "closed", "2026-10-01T00:00:00Z"), pull(2, "d", "closed", "2026-10-01T00:00:00Z")})(w, r)
	})
	s := fakeGitHub(t, mux)
	code, stdout, stderr := runWith(s, "gh", "pr", "list", "-R", "o/r", "--state", "merged", "--head", "feat/x", "-L", "2")
	want := "4\tPR 4\tb\tMERGED\t2026-10-01T00:00:00Z\n3\tPR 3\tc\tMERGED\t2026-10-01T00:00:00Z\n"
	if code != exitOK || stdout != want {
		t.Errorf("got (%d, %q, %q), want stdout %q", code, stdout, stderr, want)
	}
	if want := "… truncated, more left; raise --limit to see them\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if len(queries) != 2 || queries[0].Get("state") != "closed" || queries[0].Get("head") != "o:feat/x" ||
		queries[0].Get("per_page") != "100" {
		t.Errorf("queries = %v", queries)
	}

	code, _, stderr = runWith(s, "gh", "pr", "list", "-R", "o/r", "--state", "draft")
	if code != exitError || !strings.Contains(stderr, `invalid argument "draft" for --state`) {
		t.Errorf("bad state: got (%d, %q)", code, stderr)
	}
}

func TestPRViewJSON(t *testing.T) {
	mux := http.NewServeMux()
	pr := pull(7, "feat", "open", "")
	pr["mergeable"] = nil
	mux.Handle("GET /repos/o/r/pulls/7", reply(pr))
	mux.Handle("GET /repos/o/r/pulls/7/commits", reply([]any{map[string]any{
		"sha": "abc", "author": map[string]any{"login": "minta"},
		"commit": map[string]any{"message": "Add x\n\nBecause y", "author": map[string]any{"name": "Minta", "date": "2026-10-01T00:00:00Z"}},
	}}))
	mux.Handle("GET /repos/o/r/pulls/7/comments", reply([]any{
		map[string]any{"id": 21, "pull_request_review_id": 9, "path": "a.go", "line": 3, "body": "nit", "created_at": "2026-10-01T02:00:00Z"},
		map[string]any{"id": 22, "in_reply_to_id": 21, "pull_request_review_id": 10, "path": "a.go", "line": 3, "body": "ok", "created_at": "2026-10-01T03:00:00Z"},
	}))
	s := fakeGitHub(t, mux)
	code, stdout, stderr := runWith(s, "gh", "pr", "view", "7", "-R", "o/r", "--json", "mergeable,commits,reviewComments,state")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := decode[struct {
		State     string
		Mergeable string
		Commits   []struct {
			Oid, MessageHeadline, MessageBody string
			Authors                           []struct{ Login, Name string }
		}
		ReviewComments []struct {
			ID                  int64
			InReplyToID         *int64
			PullRequestReviewID int64
			Line                int
		}
	}](t, stdout)
	if got.State != "OPEN" || got.Mergeable != "UNKNOWN" {
		t.Errorf("state, mergeable = %q, %q", got.State, got.Mergeable)
	}
	if len(got.Commits) != 1 || got.Commits[0].MessageHeadline != "Add x" || got.Commits[0].MessageBody != "Because y" ||
		got.Commits[0].Authors[0].Login != "minta" {
		t.Errorf("commits = %+v", got.Commits)
	}
	rc := got.ReviewComments
	if len(rc) != 2 || rc[0].InReplyToID != nil || rc[1].InReplyToID == nil || *rc[1].InReplyToID != 21 ||
		rc[1].PullRequestReviewID != 10 || rc[1].Line != 3 {
		t.Errorf("reviewComments = %+v", rc)
	}
}

func TestPRViewComments(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/pulls/7", reply(pull(7, "feat", "open", "")))
	mux.Handle("GET /repos/o/r/issues/7/comments", reply([]any{
		map[string]any{"id": 1, "user": map[string]any{"login": "a"}, "body": "first", "created_at": "2026-10-01T01:00:00Z"},
	}))
	mux.Handle("GET /repos/o/r/pulls/7/reviews", reply([]any{
		map[string]any{"id": 9, "user": map[string]any{"login": "b"}, "state": "COMMENTED", "body": "", "submitted_at": "2026-10-01T02:00:00Z"},
		map[string]any{"id": 10, "user": map[string]any{"login": "c"}, "state": "APPROVED", "body": "", "submitted_at": "2026-10-01T03:00:00Z"},
		map[string]any{"id": 11, "user": map[string]any{"login": "me"}, "state": "PENDING", "body": "draft"},
	}))
	mux.Handle("GET /repos/o/r/pulls/7/comments", reply([]any{
		map[string]any{"id": 21, "user": map[string]any{"login": "b"}, "pull_request_review_id": 9, "path": "a.go", "line": 3, "body": "nit", "created_at": "2026-10-01T02:00:00Z"},
	}))
	s := fakeGitHub(t, mux)
	code, stdout, stderr := runWith(s, "gh", "pr", "view", "7", "-R", "o/r", "--comments")
	want := "kind:\tcomment\nauthor:\ta\nid:\t1\ncreated:\t2026-10-01T01:00:00Z\n--\nfirst\n--\n" +
		"kind:\treview-comment\nauthor:\tb\nid:\t21\npath:\ta.go\nline:\t3\nreview:\t9\ncreated:\t2026-10-01T02:00:00Z\n--\nnit\n--\n" +
		"kind:\treview\nauthor:\tc\nid:\t10\nstate:\tAPPROVED\ncreated:\t2026-10-01T03:00:00Z\n--\n\n--\n"
	if code != exitOK || stdout != want {
		t.Errorf("got (%d, %q, %q), want %q", code, stdout, stderr, want)
	}

	// A failed write isn't an empty conversation, so it gets no note.
	var errOut bytes.Buffer
	s.Out, s.ErrOut = failWriter{}, &errOut
	code = Run([]string{"gh", "pr", "view", "7", "-R", "o/r", "--comments"}, s)
	if want := "gh: " + io.ErrClosedPipe.Error() + "\n"; code == exitOK || errOut.String() != want {
		t.Errorf("failed write: got (%d, %q), want (non-zero, %q)", code, errOut.String(), want)
	}
}

// failWriter fails every write, like stdout with its reader gone.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestNoResults(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/issues/3", reply(testIssue))
	mux.Handle("GET /repos/o/r/pulls/7", reply(pull(7, "feat", "open", "")))
	mux.Handle("GET /repos/o/r/pulls/7/reviews", reply([]any{
		map[string]any{"id": 11, "user": map[string]any{"login": "me"}, "state": "PENDING", "body": "draft"},
	}))
	for _, p := range []string{"/repos/o/r/issues/3/comments", "/repos/o/r/issues/7/comments", "/repos/o/r/pulls/7/comments", "/repos/o/r/pulls", "/repos/o/r/labels"} {
		mux.Handle("GET "+p, reply([]any{}))
	}
	mux.Handle("GET /search/issues", reply(map[string]any{"total_count": 0, "items": []any{}}))
	s := fakeGitHub(t, mux)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"issue", "view", "3", "--comments"}, "no comments found\n"},
		{[]string{"pr", "view", "7", "--comments"}, "no comments or reviews found\n"},
		{[]string{"pr", "list"}, "no pull requests found\n"},
		{[]string{"label", "list"}, "no labels found\n"},
		{[]string{"search", "issues", "bug"}, "no results found\n"},
	}
	for _, tt := range tests {
		args := append([]string{"gh"}, tt.args...)
		args = append(args, "-R", "o/r")
		code, stdout, stderr := runWith(s, args...)
		if code != exitOK || stdout != "" || stderr != tt.want {
			t.Errorf("%v: got (%d, %q, %q), want (0, \"\", %q)", tt.args, code, stdout, stderr, tt.want)
		}
	}
	// --json says it with [], so it gets no note.
	code, stdout, stderr := runWith(s, "gh", "pr", "list", "-R", "o/r", "--json", "number")
	if code != exitOK || stdout != "[]\n" || stderr != "" {
		t.Errorf("--json: got (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestLabelList(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /repos/o/r/labels", reply([]any{
		map[string]any{"name": "good first issue", "color": "7057ff", "description": "Good for newcomers", "default": true},
	}))
	s := fakeGitHub(t, mux)
	code, stdout, _ := runWith(s, "gh", "label", "list", "-R", "o/r")
	if want := "good first issue\tGood for newcomers\t#7057ff\n"; code != exitOK || stdout != want {
		t.Errorf("got (%d, %q), want %q", code, stdout, want)
	}
	_, stdout, _ = runWith(s, "gh", "label", "list", "-R", "o/r", "--json", "url,isDefault")
	if want := `[{"isDefault":true,"url":"https://github.com/o/r/labels/good%20first%20issue"}]` + "\n"; stdout != want {
		t.Errorf("--json: got %q, want %q", stdout, want)
	}
}

func TestSearch(t *testing.T) {
	var query url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		item := map[string]any{
			"number": 3, "title": "Port", "state": "open", "repository_url": "https://api.github.com/repos/o/r",
			"pull_request": map[string]any{}, "updated_at": "2026-10-02T00:00:00Z",
		}
		reply(map[string]any{"total_count": 5000, "items": []any{item, item}})(w, r)
	})
	s := fakeGitHub(t, mux)
	code, stdout, stderr := runWith(s, "gh", "search", "prs", "out of memory", "-R", "o/r", "--state", "open", "-L", "2", "--sort", "updated")
	if want := "o/r\t3\topen\tPort\t\t2026-10-02T00:00:00Z\n"; code != exitOK || stdout != want+want {
		t.Errorf("got (%d, %q), want %q twice", code, stdout, want)
	}
	if want := "… truncated, 998 more; raise --limit to see them\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if got, want := query.Get("q"), `"out of memory" repo:o/r state:open is:pr`; got != want {
		t.Errorf("q = %q, want %q", got, want)
	}
	if query.Get("per_page") != "2" || query.Get("sort") != "updated" || query.Get("order") != "desc" {
		t.Errorf("query = %v", query)
	}
	runWith(s, "gh", "search", "issues", "repo:o/r is:open bug", "-L", "1")
	if got, want := query.Get("q"), `repo:o/r is:open bug is:issue`; got != want {
		t.Errorf("query in one argument: q = %q, want %q", got, want)
	}
	_, stdout, _ = runWith(s, "gh", "search", "issues", "x", "-L", "1", "--json", "repository,isPullRequest")
	if want := `[{"isPullRequest":true,"repository":{"nameWithOwner":"o/r"}}]` + "\n"; stdout != want {
		t.Errorf("--json: got %q, want %q", stdout, want)
	}
}

func TestSearchCommitsNeedsATerm(t *testing.T) {
	requests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/commits", func(w http.ResponseWriter, r *http.Request) {
		requests++
		reply(map[string]any{"total_count": 0, "items": []any{}})(w, r)
	})
	s := fakeGitHub(t, mux)
	// The API was checked on 2026-10-23: repo:, org: and user: alone fail
	// with 422, while author:, hash:, merge:, is: and unknown ones such as
	// feat:login search.
	for _, args := range [][]string{
		{"-R", "o/r"},
		{"--owner", "o"},
		{"--", "org:o", "NOT", "-repo:o/x"},
		{"repo:o/r user:u"},
	} {
		code, _, stderr := runWith(s, append([]string{"gh", "search", "commits"}, args...)...)
		if code != exitError || !strings.Contains(stderr, "query needs at least one search term") {
			t.Errorf("%q: got (%d, %q), want rejected", args, code, stderr)
		}
	}
	for _, args := range [][]string{
		{"fix", "-R", "o/r"},
		{`"author:x is a phrase"`, "repo:o/r"},
		{"repo:o/r", ":)"},
		{"repo:o/r", "author:minta"},
		{`author:"Minta Chiu"`, "repo:o/r"},
		{"feat:login"},
		{"repo:o/r fix"},
	} {
		if code, _, stderr := runWith(s, append([]string{"gh", "search", "commits"}, args...)...); code != exitOK {
			t.Errorf("%q: got (%d, %q), want accepted", args, code, stderr)
		}
	}
	if requests != 7 {
		t.Errorf("requests = %d, want 7", requests)
	}
}
