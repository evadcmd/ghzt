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

// secrets fakes AWS Secrets Manager.
func secrets(m map[string]string) secretFetcher {
	return func(_ context.Context, id string) ([]byte, error) {
		v, ok := m[id]
		if !ok {
			return nil, errors.New("ResourceNotFoundException")
		}
		return []byte(v), nil
	}
}

func TestFromEnvPrefersToken(t *testing.T) {
	ts, err := FromEnv(context.Background(), env(map[string]string{
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
			"no key",
			map[string]string{EnvAppID: "1", EnvInstallationID: "2"},
			"app credentials need a private key",
		},
		{
			"two keys",
			map[string]string{EnvAppID: "1", EnvInstallationID: "2", EnvPrivateKeyPath: "k.pem", EnvPrivateKeySecret: "ghzt/key"},
			"set only one of " + EnvPrivateKeyPath + " and " + EnvPrivateKeySecret,
		},
		{
			"missing key file",
			map[string]string{EnvAppID: "1", EnvInstallationID: "2", EnvPrivateKeyPath: "/nonexistent.pem"},
			"read GitHub App private key",
		},
		{
			"missing secret",
			map[string]string{EnvAppID: "1", EnvInstallationID: "2", EnvPrivateKeySecret: "ghzt/missing"},
			`fetch GitHub App private key from AWS secret "ghzt/missing": ResourceNotFoundException`,
		},
		{
			"secret is not a key",
			map[string]string{EnvAppID: "1", EnvInstallationID: "2", EnvPrivateKeySecret: "ghzt/garbage"}, //nolint:gosec // G101: a fake secret name.
			"load GitHub App private key",
		},
	}
	fetch := secrets(map[string]string{"ghzt/garbage": "not a pem"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fromEnv(context.Background(), env(tt.env), nil, fetch)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	if _, err := FromEnv(context.Background(), env(nil), nil); !errors.Is(err, ErrNoCredentials) {
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

	fetch := secrets(map[string]string{"ghzt/key": string(keyPEM)})

	for _, key := range []struct{ name, value string }{
		{EnvPrivateKeyPath, keyPath},
		{EnvPrivateKeySecret, "ghzt/key"},
	} {
		t.Run(key.name, func(t *testing.T) {
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

			ts, err := fromEnv(context.Background(), env(map[string]string{
				EnvAppID:          "7",
				EnvInstallationID: "42",
				key.name:          key.value,
			}), srv.Client().Transport, fetch)
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
		})
	}
}

func TestAWSSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Amz-Target"); got != "secretsmanager.GetSecretValue" {
			t.Errorf("X-Amz-Target = %q", got)
		}
		var in struct {
			SecretID string `json:"SecretId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]string{"Name": in.SecretID, "SecretString": "pem for " + in.SecretID})
	}))
	defer srv.Close()
	t.Setenv("AWS_ENDPOINT_URL_SECRETS_MANAGER", srv.URL)
	t.Setenv("AWS_REGION", "ap-northeast-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))

	got, err := awsSecret(context.Background(), "ghzt/key")
	if err != nil || string(got) != "pem for ghzt/key" {
		t.Errorf("awsSecret() = %q, %v; want %q", got, err, "pem for ghzt/key")
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

func TestTransportRedirect(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Authorization"))) //nolint:gosec // G705: echoes a header back to the test client.
	}
	redirect := func(mux *http.ServeMux, path, to string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, to, http.StatusFound)
		})
	}
	otherMux := http.NewServeMux()
	otherMux.HandleFunc("/", echo)
	other := httptest.NewServer(otherMux)
	defer other.Close()
	// other listens on 127.0.0.1 too; reach it as localhost to change the
	// hostname, not just the port.
	otherLocalhost := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/echo", echo)
	api := httptest.NewServer(mux)
	defer api.Close()
	for path, to := range map[string]string{
		"/same":       api.URL + "/echo",
		"/other-host": otherLocalhost + "/",
		"/other-port": other.URL + "/",
		// Once the chain has left api's host, a later hop doesn't get the
		// token back, whether it stays on the other host or returns to api.
		"/other-host-hop":  otherLocalhost + "/hop",
		"/other-host-back": otherLocalhost + "/back",
	} {
		redirect(mux, path, to)
	}
	redirect(otherMux, "/hop", otherLocalhost+"/")
	redirect(otherMux, "/back", api.URL+"/echo")

	client := &http.Client{Transport: &Transport{Source: staticToken("ghs_x")}}
	for _, tt := range []struct{ path, want string }{
		{"/echo", "Bearer ghs_x"},
		{"/same", "Bearer ghs_x"},
		{"/other-host", ""},
		{"/other-port", ""},
		{"/other-host-hop", ""},
		{"/other-host-back", ""},
	} {
		t.Run(tt.path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, api.URL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tt.want {
				t.Errorf("redirect target saw Authorization %q, want %q", body, tt.want)
			}
		})
	}
}
