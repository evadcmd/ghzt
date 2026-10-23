// Package ghapi builds the HTTP client the gh commands call the GitHub API
// with. It is built the way gh builds its own, on go-gh's client, so that
// GH_DEBUG turns on the same stderr output, in the same format, as in gh.
package ghapi

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"

	"github.com/evadcmd/ghzt/internal/auth"
)

// Options configures NewHTTPClient.
type Options struct {
	// AppVersion is ghzt's version, sent in the User-Agent header.
	AppVersion string
	// Source sets the Authorization header; requests go without one if nil.
	Source auth.TokenSource
	// Transport sends the requests; http.DefaultTransport if nil.
	Transport http.RoundTripper
	// Log is where the GH_DEBUG output goes, stderr in gh. If nil, there is
	// none whatever GH_DEBUG says.
	Log         io.Writer
	LogColorize bool
}

// NewHTTPClient returns a client for the GitHub API. It follows cli/cli's
// api.NewHTTPClient, so the debug logging, sanitizing and headers match gh's.
func NewHTTPClient(opts Options) (*http.Client, error) {
	transport := opts.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	// A host, token and transport are set so that go-gh doesn't go looking
	// for them in gh's config. The token goes in through Source instead.
	clientOpts := ghAPI.ClientOptions{
		Host:         "none",
		AuthToken:    "none",
		Transport:    hideRawType{transport},
		LogIgnoreEnv: true,
		Headers: map[string]string{
			"User-Agent":           "ghzt " + opts.AppVersion,
			"X-GitHub-Api-Version": "2022-11-28",
		},
	}
	debugEnabled, debugValue := isDebugEnabled()
	verbose := strings.Contains(debugValue, "api")
	if verbose || debugEnabled {
		clientOpts.Log = opts.Log
		clientOpts.LogColorize = opts.LogColorize
		clientOpts.LogVerboseHTTP = verbose
	}
	client, err := ghAPI.NewHTTPClient(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("create GitHub API client: %w", err)
	}
	client.Transport = restoreRawType{client.Transport}
	if opts.Source != nil {
		client.Transport = &auth.Transport{Source: opts.Source, Base: client.Transport}
	}
	return client, nil
}

// rawTypeHeader holds a raw response's Content-Type on its way through
// go-gh's transports.
const rawTypeHeader = "X-Ghzt-Raw-Content-Type"

// rawTypeRE matches the media types of the raw contents of a file, which
// GitHub may send as "application/vnd.github.raw+json".
var rawTypeRE = regexp.MustCompile(`^application/vnd\.github(\.v3)?\.raw(\+json)?$`)

// hideRawType goes under go-gh's transports. go-gh rewrites the control
// characters in any response whose Content-Type ends in json, so it can't
// smuggle terminal escapes in a JSON string; on a raw file that corrupts the
// bytes, a NUL becoming "^@". The Content-Type of a raw response is moved
// aside, so go-gh leaves the body alone, and restoreRawType puts it back.
type hideRawType struct {
	rt http.RoundTripper
}

func (h hideRawType) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := h.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	resp.Header.Del(rawTypeHeader)
	ct := resp.Header.Get("Content-Type")
	if mt, _, perr := mime.ParseMediaType(ct); perr == nil && rawTypeRE.MatchString(mt) {
		resp.Header.Del("Content-Type")
		resp.Header.Set(rawTypeHeader, ct)
	}
	return resp, nil
}

// restoreRawType goes over go-gh's transports and undoes hideRawType.
type restoreRawType struct {
	rt http.RoundTripper
}

func (r restoreRawType) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if ct := resp.Header.Get(rawTypeHeader); ct != "" {
		resp.Header.Del(rawTypeHeader)
		resp.Header.Set("Content-Type", ct)
	}
	return resp, nil
}

// isDebugEnabled is cli/cli's utils.IsDebugEnabled: GH_DEBUG if set, else the
// legacy DEBUG. The value is returned even when debugging is off, and an
// "api" in it still turns on verbose logging, as it does in gh.
func isDebugEnabled() (bool, string) {
	debugValue, isDebugSet := os.LookupEnv("GH_DEBUG")
	legacyDebugValue := os.Getenv("DEBUG")

	if !isDebugSet {
		switch legacyDebugValue {
		case "true", "1", "yes", "api":
			return true, legacyDebugValue
		default:
			return false, legacyDebugValue
		}
	}

	switch debugValue {
	case "false", "0", "no", "":
		return false, debugValue
	default:
		return true, debugValue
	}
}
