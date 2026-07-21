// Package remote is the client side of the re_gent server protocol.
//
// A Client is bound to exactly one (server URL, repo id) pair. Repo identity is
// therefore carried on every request rather than being implied by ambient
// state: two repos on the same server are two Clients, and neither can address
// the other's objects or refs.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/regent-vcs/regent/internal/server"
	"github.com/regent-vcs/regent/internal/store"
)

// maxErrorBodyBytes caps how much of an error response is quoted back.
const maxErrorBodyBytes = 4 << 10

// maxObjectBytes caps how much a client will read for a single object.
const maxObjectBytes = server.DefaultMaxObjectBytes

var (
	// ErrRepoNotFound means the server has no repo with this id yet.
	ErrRepoNotFound = errors.New("remote: repo not found")
	// ErrObjectNotFound means the object is absent from this repo on the server.
	ErrObjectNotFound = errors.New("remote: object not found")
	// ErrRefNotFound means the ref does not exist in this repo on the server.
	ErrRefNotFound = errors.New("remote: ref not found")
)

// Client talks to one repo on one re_gent server.
type Client struct {
	baseURL string
	repoID  string
	http    *http.Client
}

// NewClient validates the server URL and repo id and returns a Client bound to
// that repo. The repo id is validated with the server's own rule so a client
// can never construct an identity the server would reject.
func NewClient(baseURL, repoID string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid server url %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid server url %q: expected http:// or https://", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid server url %q: missing host", baseURL)
	}
	if err := server.ValidateRepoID(repoID); err != nil {
		return nil, err
	}
	return &Client{
		baseURL: strings.TrimSuffix(u.String(), "/"),
		repoID:  repoID,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// RepoID returns the repo this client is bound to.
func (c *Client) RepoID() string { return c.repoID }

// BaseURL returns the server root this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// SetHTTPClient overrides the HTTP client (used by tests and for custom timeouts).
func (c *Client) SetHTTPClient(h *http.Client) {
	if h != nil {
		c.http = h
	}
}

// objectURL builds the repo-scoped URL of one object.
func (c *Client) objectURL(h store.Hash) (string, error) {
	return url.JoinPath(c.baseURL, c.repoID, "objects", string(h))
}

// refURL builds the repo-scoped URL of one ref, escaping each name segment so a
// session id containing a separator cannot alter the path shape.
func (c *Client) refURL(name string) (string, error) {
	parts := append([]string{c.repoID, "refs"}, strings.Split(name, "/")...)
	return url.JoinPath(c.baseURL, parts...)
}

// EnsureRepo creates the repo on the server if it does not exist yet.
// It is idempotent and reports whether the repo was newly created.
func (c *Client) EnsureRepo(ctx context.Context) (bool, error) {
	reposURL, err := url.JoinPath(c.baseURL, "repos")
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(struct {
		RepoID string `json:"repo_id"`
	}{RepoID: c.repoID})
	if err != nil {
		return false, err
	}
	resp, err := c.do(ctx, http.MethodPost, reposURL, bytes.NewReader(body), "application/json")
	if err != nil {
		return false, err
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusCreated:
		return true, nil
	case http.StatusOK:
		return false, nil
	default:
		return false, statusError(resp, "create repo "+c.repoID)
	}
}

// HasObject reports whether this repo on the server already holds the object.
func (c *Client) HasObject(ctx context.Context, h store.Hash) (bool, error) {
	u, err := c.objectURL(h)
	if err != nil {
		return false, err
	}
	resp, err := c.do(ctx, http.MethodHead, u, nil, "")
	if err != nil {
		return false, err
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, statusError(resp, "head object "+string(h))
	}
}

// PutObject uploads one object. The upload is idempotent: re-sending an object
// the repo already holds succeeds without rewriting it.
func (c *Client) PutObject(ctx context.Context, h store.Hash, data []byte) error {
	u, err := c.objectURL(h)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, u, bytes.NewReader(data), "application/octet-stream")
	if err != nil {
		return err
	}
	defer drain(resp)

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return statusError(resp, "put object "+string(h))
	}
	return nil
}

// GetObject downloads one object from this repo.
func (c *Client) GetObject(ctx context.Context, h store.Hash) ([]byte, error) {
	u, err := c.objectURL(h)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, maxObjectBytes))
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, h)
	default:
		return nil, statusError(resp, "get object "+string(h))
	}
}

// GetRef reads one ref from this repo. A missing ref returns ErrRefNotFound;
// a repo that does not exist yet returns ErrRepoNotFound.
func (c *Client) GetRef(ctx context.Context, name string) (store.Hash, error) {
	u, err := c.refURL(name)
	if err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return "", err
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		var rr struct {
			Hash string `json:"hash"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBodyBytes)).Decode(&rr); err != nil {
			return "", fmt.Errorf("decode ref %s: %w", name, err)
		}
		return store.Hash(rr.Hash), nil
	case http.StatusNotFound:
		if unknownRepo(resp) {
			return "", fmt.Errorf("%w: %s", ErrRepoNotFound, c.repoID)
		}
		return "", fmt.Errorf("%w: %s", ErrRefNotFound, name)
	default:
		return "", statusError(resp, "get ref "+name)
	}
}

// UpdateRef performs a compare-and-swap ref update. oldHash must be the value
// the client believes the ref currently holds ("" for a ref that should not
// exist yet). A mismatch returns store.ErrRefConflict.
func (c *Client) UpdateRef(ctx context.Context, name string, oldHash, newHash store.Hash) error {
	u, err := c.refURL(name)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Old string `json:"old"`
		New string `json:"new"`
	}{Old: string(oldHash), New: string(newHash)})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, u, bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("update ref %s: %w", name, store.ErrRefConflict)
	default:
		return statusError(resp, "update ref "+name)
	}
}

// ListRefs lists the refs of this repo under dir (e.g. "sessions").
func (c *Client) ListRefs(ctx context.Context, dir string) (map[string]store.Hash, error) {
	u, err := url.JoinPath(c.baseURL, c.repoID, "refs")
	if err != nil {
		return nil, err
	}
	if dir != "" {
		u += "?dir=" + url.QueryEscape(dir)
	}
	resp, err := c.do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		var lr struct {
			Refs map[string]string `json:"refs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
			return nil, fmt.Errorf("decode ref list: %w", err)
		}
		out := make(map[string]store.Hash, len(lr.Refs))
		for k, v := range lr.Refs {
			out[k] = store.Hash(v)
		}
		return out, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrRepoNotFound, c.repoID)
	default:
		return nil, statusError(resp, "list refs")
	}
}

func (c *Client) do(ctx context.Context, method, rawURL string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, rawURL, err)
	}
	return resp, nil
}

// unknownRepo distinguishes "no such repo" from "no such ref" on a 404.
func unknownRepo(resp *http.Response) bool {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return strings.Contains(string(body), "unknown repo")
}

func statusError(resp *http.Response, what string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("%s: server returned %d: %s", what, resp.StatusCode, msg)
}

// drain closes a response body after consuming a bounded remainder so the
// connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	_ = resp.Body.Close()
}
