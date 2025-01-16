package ollama

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bmizerany/ollama-go/blob"
)

const (
	DefaultRegistryURL = "https://ollama.com"
)

// Error is the standard error returned by Ollama APIs.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return e.Message
}

// UnmarshalJSON implements json.Unmarshaler.
func (e *Error) UnmarshalJSON(b []byte) error {
	type E Error
	var v struct{ Errors []E }
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if len(v.Errors) == 0 {
		return errors.New("registry error contained empty errors array")
	}
	*e = Error(v.Errors[0]) // our registry only returns one error.
	return nil
}

// Registry is a client for performing push and pull operations against an
// Ollama registry.
type Registry struct {
	// BaseURL is the base URL of the registry.
	//
	// If empty, DefaultRegistryURL is used.
	BaseURL string

	// Key is the key used to authenticate with the registry.
	Key ed25519.PrivateKey

	// HTTPClient is the HTTP client used to make requests to the registry.
	//
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client
}

// Push pushes the model with the given name, from the given disk cache, to the
// registry.
func (r *Registry) Push(ctx context.Context, dst *blob.DiskCache, name string) error {
	panic("TODO")
}

func (r *Registry) Pull(ctx context.Context, dst *blob.DiskCache, name string) error {
	panic("TODO")
}

type Layer struct {
	Digest    blob.Digest
	MediaType string
	Size      int64
}

// Layers returns the layers of the model with the given name. If the model is
// not found, it returns an error.
//
// The returned layers are in the order they appear in the model's manifest.
func (r *Registry) Layers(ctx context.Context, name string) ([]Layer, error) {
	// TODO(bmizerany): support digest addressability
	name, tag, _ := splitNameTagDigest(name)
	res, err := r.getOK(ctx, "/v2/"+name+"/manifests/"+tag)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var m struct {
		Layers []Layer `json:"layers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m.Layers, nil
}

func (r *Registry) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func (r *Registry) getOK(ctx context.Context, path string) (*http.Response, error) {
	baseURL := r.BaseURL
	if baseURL == "" {
		baseURL = DefaultRegistryURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	// TODO(bmizerany): sign with key

	res, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		var re *Error
		if err := json.NewDecoder(res.Body).Decode(&re); err != nil {
			return nil, err
		}
		return nil, re
	}
	return res, nil
}

func splitNameTagDigest(s string) (name, tag, digest string) {
	// TODO(bmizerany): bring in a better parse from ollama.com
	s, digest, _ = strings.Cut(s, "@")
	name, tag, _ = strings.Cut(s, ":")
	return name, tag, digest
}
