// Package auth provides the GitHub token ghzt calls the API with.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/bradleyfalzon/ghinstallation/v2"
)

// Environment variables FromEnv reads.
const (
	// EnvToken is a ready-made token, e.g. a GitHub App installation token.
	EnvToken = "GHZT_TOKEN" //nolint:gosec // G101: an env var name, not a credential.
	// EnvAppID and EnvInstallationID, plus the private key from either
	// EnvPrivateKeyPath or EnvPrivateKeySecret, are a GitHub App's
	// credentials, to mint installation tokens with.
	EnvAppID          = "GHZT_APP_ID"
	EnvInstallationID = "GHZT_INSTALLATION_ID"
	EnvPrivateKeyPath = "GHZT_PRIVATE_KEY_PATH"
	// EnvPrivateKeySecret is the name or ARN of an AWS Secrets Manager secret
	// holding the PEM private key, read with the default AWS credential chain.
	EnvPrivateKeySecret = "GHZT_PRIVATE_KEY_SECRET" //nolint:gosec // G101: an env var name, not a credential.
)

// ErrNoCredentials means the environment has neither a token nor app
// credentials.
var ErrNoCredentials = fmt.Errorf("no GitHub credentials: set %s, or %s, %s and %s or %s",
	EnvToken, EnvAppID, EnvInstallationID, EnvPrivateKeyPath, EnvPrivateKeySecret)

// A secretFetcher returns the value of the secret named id.
type secretFetcher func(ctx context.Context, id string) ([]byte, error)

// awsSecret reads a secret from AWS Secrets Manager.
func awsSecret(ctx context.Context, id string) ([]byte, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	out, err := secretsmanager.NewFromConfig(cfg).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &id})
	if err != nil {
		return nil, err
	}
	if out.SecretString != nil {
		return []byte(*out.SecretString), nil
	}
	return out.SecretBinary, nil
}

// A TokenSource returns a token for the Authorization header. A source that
// mints tokens caches them and refreshes them before they expire.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

// FromEnv picks the token source from the environment, read with getenv.
//
// EnvToken wins: taruza mints a short-lived installation token and passes it
// in, so the app's private key never reaches the agent. The app credentials
// are for running ghzt by hand outside taruza, with the developer's own key
// file or AWS secret; base is the transport the installation token requests
// go through.
func FromEnv(ctx context.Context, getenv func(string) string, base http.RoundTripper) (TokenSource, error) {
	return fromEnv(ctx, getenv, base, awsSecret)
}

func fromEnv(ctx context.Context, getenv func(string) string, base http.RoundTripper, fetch secretFetcher) (TokenSource, error) {
	if token := getenv(EnvToken); token != "" {
		return staticToken(token), nil
	}
	appID, installationID := getenv(EnvAppID), getenv(EnvInstallationID)
	keyPath, keySecret := getenv(EnvPrivateKeyPath), getenv(EnvPrivateKeySecret)
	if appID == "" && installationID == "" && keyPath == "" && keySecret == "" {
		return nil, ErrNoCredentials
	}
	for _, v := range []struct{ name, value string }{
		{EnvAppID, appID},
		{EnvInstallationID, installationID},
	} {
		if v.value == "" {
			return nil, fmt.Errorf("%s is not set; app credentials need %s, %s, and %s or %s",
				v.name, EnvAppID, EnvInstallationID, EnvPrivateKeyPath, EnvPrivateKeySecret)
		}
	}
	switch {
	case keyPath == "" && keySecret == "":
		return nil, fmt.Errorf("app credentials need a private key: set %s or %s", EnvPrivateKeyPath, EnvPrivateKeySecret)
	case keyPath != "" && keySecret != "":
		return nil, fmt.Errorf("set only one of %s and %s", EnvPrivateKeyPath, EnvPrivateKeySecret)
	}
	app, err := parseID(EnvAppID, appID)
	if err != nil {
		return nil, err
	}
	installation, err := parseID(EnvInstallationID, installationID)
	if err != nil {
		return nil, err
	}
	var key []byte
	if keyPath != "" {
		key, err = os.ReadFile(keyPath) //nolint:gosec // G304: the user's own key file, named in their environment.
		if err != nil {
			return nil, fmt.Errorf("read GitHub App private key: %w", err)
		}
	} else {
		key, err = fetch(ctx, keySecret)
		if err != nil {
			return nil, fmt.Errorf("fetch GitHub App private key from AWS secret %q: %w", keySecret, err)
		}
	}
	tr, err := ghinstallation.New(base, app, installation, key)
	if err != nil {
		return nil, fmt.Errorf("load GitHub App private key: %w", err)
	}
	return tr, nil
}

func parseID(name, value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, value)
	}
	return id, nil
}

// Transport sets the Authorization header from Source on every request,
// then sends it with Base (http.DefaultTransport if nil). A redirect to
// another host goes out without the token, as gh does: GitHub redirects
// tarballs, release assets and Actions logs to storage hosts that don't need
// it, and http.Client's own stripping of Authorization on a cross-host
// redirect can't help, since the token is added here, after the Client.
type Transport struct {
	Source TokenSource
	Base   http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	// http.Client sets Response on the request it sends to follow a redirect.
	if req.Response != nil && req.Response.Request != nil && !strings.EqualFold(host(req), host(req.Response.Request)) {
		return base.RoundTrip(req)
	}
	token, err := t.Source.Token(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("get GitHub token: %w", err)
	}
	// A RoundTripper must not modify the request it was given.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return base.RoundTrip(req)
}

// host is the host req is sent to, with its port if the URL has one.
func host(req *http.Request) string {
	if req.Host != "" {
		return req.Host
	}
	return req.URL.Host
}
