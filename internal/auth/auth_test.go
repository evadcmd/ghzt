package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvPrefersToken(t *testing.T) {
	ts, err := FromEnv(env(map[string]string{
		EnvToken:          "ghs_static",
		EnvAppID:          "1",
		EnvInstallationID: "2",
		EnvPrivateKeyPath: "/nonexistent.pem",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ts.Token(context.Background())
	if err != nil || got != "ghs_static" {
		t.Errorf("Token() = %q, %v; want ghs_static", got, err)
	}
}

func TestFromEnvErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing", nil, ErrNoCredentials.Error()},
		{"partial", map[string]string{EnvAppID: "1"}, EnvInstallationID + " is not set"},
		{
			"bad app id",
			map[string]string{EnvAppID: "abc", EnvInstallationID: "2", EnvPrivateKeyPath: "k.pem"},
			EnvAppID + ` must be a positive integer, got "abc"`,
		},
		{
			"bad installation id",
			map[string]string{EnvAppID: "1", EnvInstallationID: "0", EnvPrivateKeyPath: "k.pem"},
			EnvInstallationID + ` must be a positive integer, got "0"`,
		},
		{
			"missing key file",
			map[string]string{EnvAppID: "1", EnvInstallationID: "2", EnvPrivateKeyPath: "/nonexistent.pem"},
			"load GitHub App private key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromEnv(env(tt.env), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	if _, err := FromEnv(env(nil), nil); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want ErrNoCredentials", err)
	}
}

func TestFromEnvMintsInstallationToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("Authorization = %q, want a bearer JWT", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:gosec // G101: a fake test token.
			"token":      "ghs_minted",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	ts, err := FromEnv(env(map[string]string{
		EnvAppID:          "7",
		EnvInstallationID: "42",
		EnvPrivateKeyPath: keyPath,
	}), srv.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	ts.(*ghinstallation.Transport).BaseURL = srv.URL

	for range 2 {
		got, err := ts.Token(context.Background())
		if err != nil || got != "ghs_minted" {
			t.Fatalf("Token() = %q, %v; want ghs_minted", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("token endpoint called %d times, want 1 (cached)", calls)
	}
}

type failingSource struct{}

func (failingSource) Token(context.Context) (string, error) { return "", errors.New("boom") }

func TestTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Authorization"))) //nolint:gosec // G705: echoes a header back to the test client.
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := &Transport{Source: staticToken("ghs_x"), Base: srv.Client().Transport}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "Bearer ghs_x" {
		t.Errorf("server saw Authorization %q, want %q", body, "Bearer ghs_x")
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("RoundTrip modified the caller's request")
	}

	tr.Source = failingSource{}
	if _, err := tr.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the token source's error", err)
	}
}
