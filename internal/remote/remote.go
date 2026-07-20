// Package remote defines the interface and HTTP client for communicating
// with a rgt remote server.
package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/regent-vcs/regent/internal/store"
)

// Remote is the push interface for a rgt remote server.
type Remote interface {
	// HasObject reports whether the remote already has the object.
	HasObject(h store.Hash) (bool, error)
	// SendObject uploads a blob. Idempotent.
	SendObject(h store.Hash, data []byte) error
	// UpdateRef advances a ref using compare-and-swap.
	// old may be "" when creating a new ref.
	UpdateRef(name string, old, new store.Hash) error
}

// CASConflict is returned by UpdateRef when the remote's current value does
// not match the expected old value.
type CASConflict struct {
	Name    string
	Current store.Hash
}

func (e *CASConflict) Error() string {
	return fmt.Sprintf("ref %s modified concurrently (current: %s)", e.Name, e.Current)
}

// HTTPRemote talks to a rgt server over HTTP.
type HTTPRemote struct {
	BaseURL string
	Client  *http.Client
}

// NewHTTP creates an HTTPRemote. If client is nil, http.DefaultClient is used.
func NewHTTP(baseURL string, client *http.Client) *HTTPRemote {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPRemote{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  client,
	}
}

func (r *HTTPRemote) HasObject(h store.Hash) (bool, error) {
	resp, err := r.Client.Head(r.BaseURL + "/objects/" + string(h))
	if err != nil {
		return false, fmt.Errorf("HasObject %s: %w", h, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HasObject %s: unexpected status %d", h, resp.StatusCode)
	}
}

func (r *HTTPRemote) SendObject(h store.Hash, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, r.BaseURL+"/objects/"+string(h), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("SendObject %s: build request: %w", h, err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("SendObject %s: %w", h, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SendObject %s: status %d: %s", h, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type updateRefRequest struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type refResponse struct {
	Hash string `json:"hash"`
}

func (r *HTTPRemote) UpdateRef(name string, old, new store.Hash) error {
	body, _ := json.Marshal(updateRefRequest{Old: string(old), New: string(new)})
	req, err := http.NewRequest(http.MethodPost, r.BaseURL+"/refs/"+name, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("UpdateRef %s: build request: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("UpdateRef %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		var cur refResponse
		_ = json.NewDecoder(resp.Body).Decode(&cur)
		return &CASConflict{Name: name, Current: store.Hash(cur.Hash)}
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("UpdateRef %s: status %d: %s", name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
}
