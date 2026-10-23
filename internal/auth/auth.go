// Package auth provides the GitHub token ghzt calls the API with.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

// Environment variables FromEnv reads.
const (
	// EnvToken is a ready-made token, e.g. a GitHub App installation token.
	EnvToken = "GHZT_TOKEN" //nolint:gosec // G101: an env var name, not a credential.
	// EnvAppID, EnvInstallationID and EnvPrivateKeyPath are a GitHub App's
	// credentials, to mint installation tokens with.
	EnvAppID          = "GHZT_APP_ID"
	EnvInstallationID = "GHZT_INSTALLATION_ID"
	EnvPrivateKeyPath = "GHZT_PRIVATE_KEY_PATH"
)

// ErrNoCredentials means the environment has neither a token nor app
// credentials.
var ErrNoCredentials = errors.New("no GitHub credentials: set " + EnvToken +
	", or " + EnvAppID + ", " + EnvInstallationID + " and " + EnvPrivateKeyPath)

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
// file; base is the transport the installation token requests go through.
func FromEnv(getenv func(string) string, base http.RoundTripper) (TokenSource, error) {
	if token := getenv(EnvToken); token != "" {
		return staticToken(token), nil
	}
	appID, installationID, keyPath := getenv(EnvAppID), getenv(EnvInstallationID), getenv(EnvPrivateKeyPath)
	if appID == "" && installationID == "" && keyPath == "" {
		return nil, ErrNoCredentials
	}
	for _, v := range []struct{ name, value string }{
		{EnvAppID, appID},
		{EnvInstallationID, installationID},
		{EnvPrivateKeyPath, keyPath},
	} {
		if v.value == "" {
			return nil, fmt.Errorf("%s is not set; app credentials need all of %s, %s and %s",
				v.name, EnvAppID, EnvInstallationID, EnvPrivateKeyPath)
		}
	}
	app, err := parseID(EnvAppID, appID)
	if err != nil {
		return nil, err
	}
	installation, err := parseID(EnvInstallationID, installationID)
	if err != nil {
		return nil, err
	}
	tr, err := ghinstallation.NewKeyFromFile(base, app, installation, keyPath)
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
// then sends it with Base (http.DefaultTransport if nil).
type Transport struct {
	Source TokenSource
	Base   http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
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
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
