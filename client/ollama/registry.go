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
	"runtime"
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

// newRequest returns a new http.Request with the given method, and body. The
// path is appended to the registry's BaseURL to make the full URL.
func (r *Registry) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	baseURL := r.BaseURL
	if baseURL == "" {
		baseURL = DefaultRegistryURL
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)
	return req, nil
}

// Push pushes the model with the name in the cache to the remote registry.
func (r *Registry) Push(ctx context.Context, c *blob.DiskCache, name string) error {
	panic("TODO")
}

func makeBlobPath(name string, d blob.Digest) string {
	shortName, _, _ := splitNameTagDigest(name)
	return "/v2/" + shortName + "/blobs/" + d.String()
}

// Pull pulls the model with the given name from the remote registry into the
// cache.
func (r *Registry) Pull(ctx context.Context, c *blob.DiskCache, name string) error {
	exists := func(l layer) bool {
		info, err := c.Get(l.Digest)
		return err == nil && info.Size == l.Size
	}
	download := func(l layer) error {
		blobPath := makeBlobPath(name, l.Digest)
		req, err := r.newRequest(ctx, "GET", blobPath, nil)
		if err != nil {
			return err
		}
		res, err := r.client().Do(req)
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

	g := newGroup(runtime.GOMAXPROCS(0)) // TODO(bmizerany): make this configurable?
	for l, err := range r.layers(ctx, name) {
		if err != nil {
			return err
		}
		if !exists(l) {
			g.do(func() error { return download(l) })
		}
	}
	return g.wait()
}

type layer struct {
	Digest    blob.Digest
	MediaType string
	Size      int64
}

// layers returns the layers of the model with the given name. If the model is
// not found, it returns an error.
//
// The returned layers are in the order they appear in the model's manifest.
func (r *Registry) layers(ctx context.Context, name string) iter.Seq2[layer, error] {
	return func(yield func(layer, error) bool) {
		// TODO(bmizerany): support digest addressability
		shortName, tag, _ := splitNameTagDigest(name)
		res, err := r.getOK(ctx, "/v2/"+shortName+"/manifests/"+tag)
		if err != nil {
			yield(layer{}, err)
			return
		}
		defer res.Body.Close()
		var m struct {
			Layers []layer `json:"layers"`
		}
		if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
			yield(layer{}, err)
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
	req, err := r.newRequest(ctx, http.MethodGet, path, nil)
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

// group manages a group of goroutines, limiting the number of concurrent
// goroutines to n, and collecting any errors they return.
type group struct {
	sem chan struct{}
	wg  sync.WaitGroup

	mu   sync.Mutex
	errs []error
}

// newGroup returns a new group limited to n goroutines.
func newGroup(n int) *group {
	return &group{sem: make(chan struct{}, n)}
}

// do runs the given function f in a goroutine, blocking if the group is at its
// limit. Any error returned by f is recorded and returned in the next call to
// [group.wait].
//
// All calls to do should be made before calling [group.wait]. If a call to do
// happens after [group.wait], the behavior is undefined.
//
// It is not safe for concurrent use.
func (g *group) do(f func() error) {
	g.wg.Add(1)
	g.sem <- struct{}{}
	go func() {
		defer func() {
			<-g.sem
			g.wg.Done()
		}()
		if err := f(); err != nil {
			g.mu.Lock()
			g.errs = append(g.errs, err)
			g.mu.Unlock()
		}
	}()
}

// wait waits for all running goroutines in the group to finish. All errors are
// returned using [errors.Join].
func (g *group) wait() error {
	// no need to guard g.errs here, since we know all goroutines have
	// finished.
	g.wg.Wait()
	return errors.Join(g.errs...)
}
