package ollama

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmizerany/ollama-go/blob"
)

const (
	DefaultRegistryURL = "https://ollama.com"
)

// delay creating the default cache until it is needed, if ever.
var defaultCache = sync.OnceValue(func() *blob.DiskCache {
	c, err := cacheFromEnv()
	if err != nil {
		panic(err)
	}
	return c
})

func cacheFromEnv() (*blob.DiskCache, error) {
	dir := os.Getenv("OLLAMA_MODELS")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		dir = filepath.Join(home, ".ollama", "models")
	}
	return blob.Open(dir)
}

type RegistryError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *RegistryError) Error() string {
	return e.Message
}

func (e *RegistryError) UnmarshalJSON(b []byte) error {
	type E RegistryError
	var v struct{ Errors []E }
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if len(v.Errors) == 0 {
		return errors.New("registry error contained empty errors array")
	}
	*e = RegistryError(v.Errors[0]) // only use the first error
	return nil
}

type Registry struct {
	// BaseURL is the base URL of the registry.
	//
	// If empty, DefaultRegistryURL is used.
	BaseURL string

	// Key is the key used to authenticate with the registry.
	Key ed25519.PrivateKey

	// Cache is the cache used to store blobs.
	Cache *blob.DiskCache

	// HTTPClient is the HTTP client used to make requests to the registry.
	//
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client
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
	if res.StatusCode != 200 {
		var re *RegistryError
		if err := json.NewDecoder(res.Body).Decode(&re); err != nil {
			return nil, err
		}
		return nil, re
	}
	var m struct {
		Layers []Layer `json:"layers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m.Layers, nil
}

func (r *Registry) getOK(ctx context.Context, path string) (*http.Response, error) {
	c := r.HTTPClient
	if c == nil {
		c = http.DefaultClient
	}

	baseURL := r.BaseURL
	if baseURL == "" {
		baseURL = DefaultRegistryURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	// TODO(bmizerany): sign with key
	return c.Do(req)
}

func splitNameTagDigest(s string) (name, tag, digest string) {
	// TODO(bmizerany): bring in a better parse from ollama.com
	s, digest, _ = strings.Cut(s, "@")
	name, tag, _ = strings.Cut(s, ":")
	return name, tag, digest
}
