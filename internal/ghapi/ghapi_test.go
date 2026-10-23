package ghapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

// setenv sets the variables in m for the test, unsetting those whose value is
// nil.
func setenv(t *testing.T, m map[string]*string) {
	t.Helper()
	for k, v := range m {
		t.Setenv(k, "") // registers the cleanup that restores k
		if v == nil {
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Setenv(k, *v)
	}
}

func ptr(s string) *string { return &s }

func TestIsDebugEnabled(t *testing.T) {
	tests := []struct {
		ghDebug, debug *string
		want           bool
		wantValue      string
	}{
		{nil, nil, false, ""},
		{ptr("1"), nil, true, "1"},
		{ptr("api"), nil, true, "api"},
		{ptr("anything"), nil, true, "anything"},
		{ptr("0"), nil, false, "0"},
		{ptr("false"), nil, false, "false"},
		{ptr("no"), nil, false, "no"},
		// A set GH_DEBUG wins over DEBUG, even when it's empty.
		{ptr(""), ptr("1"), false, ""},
		{nil, ptr("1"), true, "1"},
		{nil, ptr("api"), true, "api"},
		{nil, ptr("anything"), false, "anything"},
	}
	for _, tt := range tests {
		setenv(t, map[string]*string{"GH_DEBUG": tt.ghDebug, "DEBUG": tt.debug})
		got, value := isDebugEnabled()
		if got != tt.want || value != tt.wantValue {
			t.Errorf("GH_DEBUG=%v DEBUG=%v: isDebugEnabled() = %v, %q; want %v, %q",
				deref(tt.ghDebug), deref(tt.debug), got, value, tt.want, tt.wantValue)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "(unset)"
	}
	return *s
}

// get makes a request to a test server with a client built from opts and
// returns the Authorization header the server got.
func get(t *testing.T, opts Options) string {
	t.Helper()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"login":"octocat"}`)
	}))
	defer srv.Close()

	client, err := NewHTTPClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/user")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	return gotAuth
}

func TestNewHTTPClientLog(t *testing.T) {
	tests := []struct {
		ghDebug     *string
		wantLog     bool
		wantVerbose bool
	}{
		{nil, false, false},
		{ptr("0"), false, false},
		{ptr("1"), true, false},
		{ptr("api"), true, true},
	}
	for _, tt := range tests {
		setenv(t, map[string]*string{"GH_DEBUG": tt.ghDebug, "DEBUG": nil})
		var log bytes.Buffer
		gotAuth := get(t, Options{Source: staticToken("ghs_secret"), Log: &log})

		if gotAuth != "Bearer ghs_secret" {
			t.Errorf("GH_DEBUG=%s: server got Authorization %q", deref(tt.ghDebug), gotAuth)
		}
		out := log.String()
		if got := strings.Contains(out, "/user"); got != tt.wantLog {
			t.Errorf("GH_DEBUG=%s: logged the request = %v, want %v; log:\n%s", deref(tt.ghDebug), got, tt.wantLog, out)
		}
		if got := strings.Contains(out, `"login": "octocat"`); got != tt.wantVerbose {
			t.Errorf("GH_DEBUG=%s: logged the body = %v, want %v; log:\n%s", deref(tt.ghDebug), got, tt.wantVerbose, out)
		}
		if strings.Contains(out, "ghs_secret") {
			t.Errorf("GH_DEBUG=%s: the token is in the log:\n%s", deref(tt.ghDebug), out)
		}
	}
}

func TestNewHTTPClientSanitize(t *testing.T) {
	tests := []struct {
		contentType, body, want string
	}{
		// go-gh's sanitizing stays on for JSON...
		{"application/json; charset=utf-8", `{"name":"a\u001b[31m"}`, `{"name":"a^[[31m"}`},
		// ...but a raw file comes through byte for byte, whatever it holds.
		{"application/vnd.github.raw+json; charset=utf-8", "a\x00b\x1b[31m\\u0000", "a\x00b\x1b[31m\\u0000"},
		{"application/vnd.github.v3.raw", "a\x00b", "a\x00b"},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", tt.contentType)
			w.Header().Set(rawTypeHeader, "text/plain") // not the server's to set
			_, _ = io.WriteString(w, tt.body)
		}))
		client, err := NewHTTPClient(Options{})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.want {
			t.Errorf("%s: body = %q, want %q", tt.contentType, got, tt.want)
		}
		if ct := resp.Header.Get("Content-Type"); ct != tt.contentType {
			t.Errorf("%s: Content-Type = %q", tt.contentType, ct)
		}
		if h := resp.Header.Get(rawTypeHeader); h != "" {
			t.Errorf("%s: %s = %q, want it removed", tt.contentType, rawTypeHeader, h)
		}
	}
}

func TestNewHTTPClientNoLogWriter(t *testing.T) {
	t.Setenv("GH_DEBUG", "api")
	// Nothing to assert on but that it doesn't panic or write anywhere else.
	get(t, Options{})
}
