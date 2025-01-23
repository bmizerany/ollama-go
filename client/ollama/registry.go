package ollama

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	// MaxStreams is the maximum number of concurrent streams to use when
	// pushing or pulling models. If zero, the number of streams is
	// determined by [runtime.GOMAXPROCS].
	//
	// Clients that want "unlimited" streams should set this to a large
	// number.
	MaxStreams int
}

// Push pushes the model with the name in the cache to the remote registry.
func (r *Registry) Push(ctx context.Context, c *blob.DiskCache, name string) error {
	// resolve the name to a its manifest
	m, err := r.Resolve(ctx, name)
	if err != nil {
		return err
	}

	t := traceFromContext(ctx)

	// TODO(bmizerany): backoff and retry with resumable uploads (need to
	// ask server how far along we are)
	upload := func(l Layer) error {
		// TODO(bmizerany): check with HEAD first to see if we need to upload
		startUploadPath := fmt.Sprintf("/v2/%s/blobs/uploads?digest=%s", name, l.Digest)
		res, err := r.doOK(ctx, "POST", startUploadPath, nil)
		if err != nil {
			return err
		}
		res.Body.Close()
		uploadURL := res.Header.Get("Location")

		f, err := os.Open(c.GetFile(l.Digest))
		if err != nil {
			return err
		}
		defer f.Close()

		tr := &traceReader{l: l, r: f, fn: t.uploadUpdate}
		res, err = r.doOK(ctx, "POST", uploadURL, tr)
		if err != nil {
			return err
		}
		res.Body.Close()
		return nil
	}

	g := newGroup(r.MaxStreams)
	for _, l := range m.Layers {
		g.do(func() error { return upload(l) })
	}

	name, tag, _ := splitNameTagDigest(name)
	path := fmt.Sprintf("/v2/%s/manifests/%s", name, tag)
	res, err := r.doOK(ctx, "PUT", path, bytes.NewReader(m.Data))
	if res != nil {
		res.Body.Close()
	}
	return err
}

// Pull pulls the model with the given name from the remote registry into the
// cache.
func (r *Registry) Pull(ctx context.Context, c *blob.DiskCache, name string) error {
	t := traceFromContext(ctx)

	exists := func(l Layer) bool {
		info, err := c.Get(l.Digest)
		return err == nil && info.Size == l.Size
	}
	download := func(l Layer) error {
		blobPath := makeBlobPath(name, l.Digest)
		res, err := r.doOK(ctx, "GET", blobPath, nil)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.ContentLength != l.Size {
			return &Error{Code: "DOWNLOAD_ERROR", Message: "size mismatch"}
		}
		tr := &traceReader{l: l, r: res.Body, fn: t.downloadUpdate}
		return c.Put(l.Digest, tr, l.Size)
	}

	// resolve the name to a its manifest
	m, err := r.Resolve(ctx, name)
	if err != nil {
		return err
	}
	md := blob.DigestFromBytes(m.Data)
	if t.Resolved != nil {
		t.Resolved(name, md)
	}

	// download all the layers and put them in the cache
	g := newGroup(r.MaxStreams) // TODO(bmizerany): make this configurable?
	for _, l := range m.Layers {
		if exists(l) {
			t.downloadUpdate(l.Digest, l.Size, l.Size, nil)
		} else {
			g.do(func() error { return download(l) })
		}
	}
	if err := g.wait(); err != nil {
		return err
	}

	// store the manifest blob
	if err := blob.PutBytes(c, md, m.Data); err != nil {
		return err
	}

	// commit the manifest with a link
	return c.Link(m.Name, md)
}

// Manifest is the manifest of a model.
type Manifest struct {
	Name   string  `json:"-"` // the cananical name of the model
	Data   []byte  `json:"-"` // the raw data of the manifest
	Layers []Layer `json:"layers"`
}

// Layer is a layer in a model.
type Layer struct {
	Digest    blob.Digest
	MediaType string
	Size      int64
}

// Resolve returns the Resolve of the model with the given name. If the model is
// not found, it returns an error.
//
// The returned Resolve are in the order they appear in the model's manifest.
func (r *Registry) Resolve(ctx context.Context, name string) (*Manifest, error) {
	// TODO(bmizerany): support digest addressability
	shortName, tag, _ := splitNameTagDigest(name)
	res, err := r.doOK(ctx, "GET", "/v2/"+shortName+"/manifests/"+tag, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	m.Data = data

	// TODO(bmizerany): The name should be taken from the res.Request since
	// it _should_ be the canonical name; however, the registry doesn't
	// redirect to the canonical name right now. It will soon, and when it
	// does, we update this.
	m.Name = res.Request.URL.Host + "/" + name

	t := traceFromContext(ctx)
	if t.Resolved != nil {
		t.Resolved(name, blob.DigestFromBytes(data))
	}

	return &m, nil
}

func (r *Registry) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
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

func (r *Registry) doOK(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := r.newRequest(ctx, method, path, body)
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
	if n == 0 {
		n = runtime.GOMAXPROCS(0)
	}
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

func makeBlobPath(name string, d blob.Digest) string {
	shortName, _, _ := splitNameTagDigest(name)
	return "/v2/" + shortName + "/blobs/" + d.String()
}
