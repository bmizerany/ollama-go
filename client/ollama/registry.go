package ollama

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmizerany/ollama-go/blob"

	_ "embed"
)

// DefaultCache returns a new disk cache for storing models. If the
// OLLAMA_MODELS environment variable is set, it uses that directory;
// otherwise, it uses $HOME/.ollama/models.
func DefaultCache() (*blob.DiskCache, error) {
	dir := os.Getenv("OLLAMA_MODELS")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".ollama", "models")
	}
	os.MkdirAll(dir, 0755)
	return blob.Open(dir)
}

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

	// UserAgent is the User-Agent header to send with requests to the
	// registry. If empty, DefaultUserAgent is used.
	UserAgent string

	// Key is the key used to authenticate with the registry.
	Key ed25519.PrivateKey

	// HTTPClient is the HTTP client used to make requests to the registry.
	//
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client
}

// Push pushes the model with the name in the cache to the remote registry.
func (r *Registry) Push(ctx context.Context, c *blob.DiskCache, name string) error {
	panic("TODO")
}

// Pull pulls the model with the given name from the remote registry into the
// cache.
func (r *Registry) Pull(ctx context.Context, c *blob.DiskCache, name string) error {
	exists := func(l Layer) bool {
		info, err := c.Get(l.Digest)
		return err == nil && info.Size == l.Size
	}
	blobURL := func(d blob.Digest) (_ string, _ error) {
		shortName, _, _ := splitNameTagDigest(name)
		res, err := r.getOK(ctx, "/v2/"+shortName+"/blobs/"+d.String())
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		if res.ContentLength == 0 {
			return "", &Error{Code: "DOWNLOAD_ERROR", Message: "no content length"}
		}
		return res.Request.URL.String(), nil
	}
	download := func(l Layer) error {
		loc, err := blobURL(l.Digest)
		if err != nil {
			return err
		}
		res, err := r.client().Get(loc)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.ContentLength != l.Size {
			return &Error{Code: "DOWNLOAD_ERROR", Message: "size mismatch"}
		}
		if res.StatusCode != 200 {
			return &Error{Code: "DOWNLOAD_ERROR", Message: res.Status}
		}
		return c.Put(l.Digest, res.Body, l.Size)
	}

	var g sync.WaitGroup
	var errs []error
	for l, err := range r.layers(ctx, name) {
		if err != nil {
			return err
		}
		if exists(l) {
			continue
		}
		g.Add(1)
		errs = append(errs, nil)
		i := len(errs) - 1
		go func() {
			defer g.Done()
			if err := download(l); err != nil {
				errs[i] = err
				return
			}
		}()
	}
	g.Wait()
	return errors.Join(errs...)
}

type Layer struct {
	Digest    blob.Digest
	MediaType string
	Size      int64
}

// layers returns the layers of the model with the given name. If the model is
// not found, it returns an error.
//
// The returned layers are in the order they appear in the model's manifest.
func (r *Registry) layers(ctx context.Context, name string) iter.Seq2[Layer, error] {
	return func(yield func(Layer, error) bool) {
		// TODO(bmizerany): support digest addressability
		shortName, tag, _ := splitNameTagDigest(name)
		res, err := r.getOK(ctx, "/v2/"+shortName+"/manifests/"+tag)
		if err != nil {
			yield(Layer{}, err)
			return
		}
		defer res.Body.Close()
		var m struct {
			Layers []Layer `json:"layers"`
		}
		if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
			yield(Layer{}, err)
			return
		}
		for _, l := range m.Layers {
			if !yield(l, nil) {
				return
			}
		}
	}
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
		out, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}
		var re Error
		if err := json.Unmarshal(out, &re); err != nil {
			return nil, &Error{Message: string(out)}
		}
		return nil, &re
	}
	return res, nil
}

func splitNameTagDigest(s string) (shortName, tag, digest string) {
	// TODO(bmizerany): bring in a better parse from ollama.com
	s, digest, _ = strings.Cut(s, "@")
	shortName, tag, _ = strings.Cut(s, ":")
	return shortName, tag, digest
}
