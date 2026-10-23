package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"

	"github.com/evadcmd/ghzt/internal/auth"
	"github.com/evadcmd/ghzt/internal/ghapi"
)

// apiURL is the REST API root. Tests don't change it: they send the requests
// to an httptest server with IO.Transport instead.
const apiURL = "https://api.github.com"

// maxPerPage is the largest page the REST API returns.
const maxPerPage = 100

// apiClient makes requests to the REST API.
type apiClient struct {
	http *http.Client
}

// newAPIClient builds the client from the credentials in the environment. It
// is called when a command runs, not when the tree is built, so --help and
// flag errors work without credentials.
func newAPIClient(ctx context.Context, s IO) (*apiClient, error) {
	transport := s.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	source, err := auth.FromEnv(ctx, s.getenv, transport)
	if err != nil {
		return nil, err
	}
	client, err := ghapi.NewHTTPClient(ghapi.Options{
		AppVersion: version,
		Source:     source,
		Transport:  transport,
		Log:        s.ErrOut,
	})
	if err != nil {
		return nil, err
	}
	return &apiClient{http: client}, nil
}

// get decodes the JSON response to GET path?query into v and returns the
// response headers, for the Link header pagination follows.
func (c *apiClient) get(ctx context.Context, path string, query url.Values, v any) (http.Header, error) {
	resp, err := c.do(ctx, path, query, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return nil, fmt.Errorf("decode response to %s %s: %w", http.MethodGet, path, err)
	}
	return resp.Header, nil
}

// getRaw returns the body of GET path?query, asking for accept, and its
// headers. A body over maxBytes is an error rather than cut short.
func (c *apiClient) getRaw(ctx context.Context, path string, query url.Values, accept string, maxBytes int64) ([]byte, http.Header, error) {
	resp, err := c.do(ctx, path, query, accept)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read response to %s %s: %w", http.MethodGet, path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, fmt.Errorf("response to %s %s is larger than %d bytes", http.MethodGet, path, maxBytes)
	}
	return data, resp.Header, nil
}

// send makes a write request: body, if not nil, goes as JSON, and the JSON
// response is decoded into v, if not nil.
func (c *apiClient) send(ctx context.Context, method, path string, body, v any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	resp, err := c.request(ctx, method, path, nil, "application/vnd.github+json", r)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if v == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode response to %s %s: %w", method, path, err)
	}
	return nil
}

// do sends GET path?query and returns the response if it is a success; the
// caller closes its body.
func (c *apiClient) do(ctx context.Context, path string, query url.Values, accept string) (*http.Response, error) {
	return c.request(ctx, http.MethodGet, path, query, accept, nil)
}

// request sends method path?query with body, a JSON document if not nil, and
// returns the response if it is a success; the caller closes its body.
func (c *apiClient) request(ctx context.Context, method, path string, query url.Values, accept string, body io.Reader) (*http.Response, error) {
	u := apiURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		return nil, apiError(resp, method, path)
	}
	return resp, nil
}

// apiError turns an error response into one line: the status, GitHub's
// messages joined with "; ", and the request without the host or query. A
// rate limit says when it resets, which is what the caller needs to know.
func apiError(resp *http.Response, method, path string) error {
	msg := resp.Status
	if herr, ok := errors.AsType[*ghAPI.HTTPError](ghAPI.HandleHTTPError(resp)); ok && herr.Message != "" {
		msg = strings.ReplaceAll(herr.Message, "\n", "; ")
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			msg = "API rate limit exceeded"
			if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				msg += ", resets at " + time.Unix(reset, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	return fmt.Errorf("HTTP %d: %s (%s %s)", resp.StatusCode, msg, method, path)
}

// hasNextPage reports whether the Link header points to another page.
func hasNextPage(h http.Header) bool {
	return strings.Contains(h.Get("Link"), `rel="next"`)
}

// list fetches up to limit items from a list endpoint, all of them if limit
// is 0, and reports whether more were left. keep, if not nil, drops items,
// for filters the endpoint doesn't have; pages are then always full size, as
// a page of limit items might keep fewer.
func list[T any](ctx context.Context, c *apiClient, path string, query url.Values, limit int, keep func(T) bool) ([]T, bool, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	perPage := maxPerPage
	if limit > 0 && limit < perPage && keep == nil {
		perPage = limit
	}
	q.Set("per_page", strconv.Itoa(perPage))
	var items []T
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		var batch []T
		h, err := c.get(ctx, path, q, &batch)
		if err != nil {
			return nil, false, err
		}
		for _, item := range batch {
			if keep != nil && !keep(item) {
				continue
			}
			if limit > 0 && len(items) == limit {
				return items, true, nil
			}
			items = append(items, item)
		}
		if !hasNextPage(h) || len(batch) == 0 {
			return items, false, nil
		}
		// With keep, the next page might hold nothing it keeps; fetching on
		// to find out could take every remaining page, so it counts as more.
		if limit > 0 && len(items) == limit {
			return items, true, nil
		}
	}
}

// searchResult is a page of the search API.
type searchResult[T any] struct {
	TotalCount int `json:"total_count"`
	Items      []T `json:"items"`
}

// maxSearchResults is how far the search API pages: it returns at most the
// first 1000 results of a query.
const maxSearchResults = 1000

// search runs query against /search/<kind> and returns up to limit items and
// the total number of results.
func search[T any](ctx context.Context, c *apiClient, kind string, query url.Values, limit int) ([]T, int, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	limit = min(limit, maxSearchResults)
	q.Set("per_page", strconv.Itoa(min(limit, maxPerPage)))
	var items []T
	total := 0
	for page := 1; len(items) < limit; page++ {
		q.Set("page", strconv.Itoa(page))
		var res searchResult[T]
		if _, err := c.get(ctx, "/search/"+kind, q, &res); err != nil {
			return nil, 0, err
		}
		total = res.TotalCount
		items = append(items, res.Items...)
		if len(res.Items) == 0 || len(items) >= total {
			break
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, total, nil
}
