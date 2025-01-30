package ollama

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/internal/names"
	"github.com/bmizerany/ollama-go/internal/syncs"
	"golang.org/x/crypto/ssh"

	_ "embed"
)

// Errors
var (
	ErrManifestNotFound = errors.New("manifest not found")
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
	BaseURL string // TODO(bmizerany): remove and use host from model names

	// UserAgent is the User-Agent header to send with requests to the
	// registry. If empty, DefaultUserAgent is used.
	UserAgent string

	// Key is the key used to authenticate with the registry.
	//
	// Currently, only Ed25519 keys are supported.
	Key crypto.PrivateKey

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

func RegistryFromEnv() (*Registry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(home, ".ollama/id_ed25519"))
	if err != nil {
		return nil, err
	}
	key, err := ssh.ParseRawPrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}
	baseURL := cmp.Or(os.Getenv("OLLAMA_REGISTRY"), DefaultRegistryURL)
	rc := &Registry{
		BaseURL: baseURL,
		Key:     key,
	}
	return rc, nil
}

type PushParams struct {
	// From is an optional destination name for the model. If empty, the
	// destination name is the same as the source name.
	From string
}

func cacheResolve(c *blob.DiskCache, name string) (*Manifest, error) {
	d, err := c.Resolve(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrManifestNotFound, name)
		}
		return nil, err
	}
	data, err := os.ReadFile(c.GetFile(d))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m.Data = data
	return &m, nil
}

// Push pushes the model with the name in the cache to the remote registry.
func (r *Registry) Push(ctx context.Context, c *blob.DiskCache, name string, p *PushParams) error {
	from := cmp.Or(p.From, name)
	m, err := cacheResolve(c, from)
	if err != nil {
		return err
	}

	if p == nil {
		p = &PushParams{}
	}

	t := traceFromContext(ctx)

	// split the name into its short name, tag, and digest, preferring the
	// destination name if provided.
	name, tag, _ := splitNameTagDigest(name)

	// TODO(bmizerany): backoff and retry with resumable uploads (need to
	// ask server how far along we are)
	upload := func(l *Layer) error {
		t.pushUpdate(l.Digest, 0, l.Size, nil) // initial update

		startUploadPath := fmt.Sprintf("/v2/%s/blobs/uploads/", name)
		res, err := r.doOK(ctx, "POST", startUploadPath, nil)
		if err != nil {
			return err
		}
		res.Body.Close()

		// Perform a "monolithic" upload for all layers. They should be
		// diced up on "create/import" so we need not do fancy things
		// like chunked, resumable uploads here.
		uploadURL, err := func() (string, error) {
			u, err := res.Location()
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("digest", l.Digest.String())
			u.RawQuery = q.Encode()
			return u.String(), nil
		}()
		if err != nil {
			return err
		}

		f, err := os.Open(c.GetFile(l.Digest))
		if err != nil {
			return err
		}
		defer f.Close()

		tr := &traceReader{l: l, r: f, fn: t.pushUpdate}
		req, err := r.newRequest(ctx, "PUT", uploadURL, tr)
		if err != nil {
			return err
		}
		req.ContentLength = l.Size

		res, err = doOK(r.client(), req)
		if err != nil {
			return fmt.Errorf("push: %s %s: %w", name, l.Digest.Short(), err)
		}
		res.Body.Close()

		// We got 2XX, so we're done with this layer, but if we did not
		// upload the whole thing, that means it already existed, so
		// say that in our update.
		if tr.n < l.Size {
			t.pushUpdate(l.Digest, l.Size, l.Size, nil)
		}

		return nil
	}

	g := syncs.NewGroup(r.MaxStreams)
	for _, l := range m.Layers {
		g.Go(func() error { return upload(l) })
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("push: %s: %w", name, err)
	}

	path := fmt.Sprintf("/v2/%s/manifests/%s", name, tag)
	res, err := r.doOK(ctx, "PUT", path, bytes.NewReader(m.Data))
	if err != nil {
		return fmt.Errorf("push: %s: %w", name, err)
	}
	res.Body.Close()
	return nil
}

// Pull pulls the model with the given name from the remote registry into the
// cache.
func (r *Registry) Pull(ctx context.Context, c *blob.DiskCache, name string) error {
	t := traceFromContext(ctx)

	exists := func(l *Layer) bool {
		info, err := c.Get(l.Digest)
		return err == nil && info.Size == l.Size
	}

	download := func(l *Layer) error {
		t.pullUpdate(l.Digest, 0, l.Size, nil) // initial update
		if exists(l) {
			t.pullUpdate(l.Digest, l.Size, l.Size, nil)
			return nil
		}

		blobPath := makeBlobPath(name, l.Digest)
		res, err := r.doOK(ctx, "GET", blobPath, nil)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		tr := &traceReader{l: l, r: res.Body, fn: t.pullUpdate}
		return c.Put(l.Digest, tr, l.Size)
	}

	// resolve the name to a its manifest
	m, err := r.Resolve(ctx, name)
	if err != nil {
		return err
	}
	if len(m.Layers) == 0 {
		return fmt.Errorf("pull: %s: no layers", name)
	}

	// download all the layers and put them in the cache
	g := syncs.NewGroup(r.MaxStreams)
	for _, l := range m.Layers {
		g.Go(func() error {
			err := download(l)
			if err != nil {
				return fmt.Errorf("%s %s: %w", name, l.Digest.Short(), err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// store the manifest blob
	md := blob.DigestFromBytes(m.Data)
	if err := blob.PutBytes(c, md, m.Data); err != nil {
		return err
	}

	// commit the manifest with a link
	return c.Link(m.Name, md)
}

// Manifest is the manifest of a model.
type Manifest struct {
	Name   string   `json:"-"` // the cananical name of the model
	Data   []byte   `json:"-"` // the raw data of the manifest
	Layers []*Layer `json:"layers"`
}

var emptyDigest, _ = blob.ParseDigest("sha256:0000000000000000000000000000000000000000000000000000000000000000")

func (m Manifest) LookupLayer(d blob.Digest) *Layer {
	for _, l := range m.Layers {
		if l.Digest == d {
			return l
		}
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
//
// NOTE: It adds an empty config object to the manifest, which is required by
// the registry, but not used by the client. In the future, the config object
// will not be required by the registry and this will should be removed.
func (m Manifest) MarshalJSON() ([]byte, error) {
	type M Manifest
	var v = struct {
		M

		// This is ignored, mostly, by the registry But, if not
		// present, it will cause an error to be returned during the
		// last phase of the commit which expects it, but does nothing
		// with it. This will be fixed in a future release of
		// ollama.com.
		Config *Layer `json:"config"`
	}{
		M:      M(m),
		Config: &Layer{Digest: emptyDigest},
	}
	return json.Marshal(v)
}

func unmarshalManifest(name string, data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m.Name = name
	m.Data = data
	return &m, nil
}

// Layer is a layer in a model.
type Layer struct {
	Digest    blob.Digest `json:"digest"`
	MediaType string      `json:"mediaType"`
	Size      int64       `json:"size"`
}

// Resolve resolves a name to a Manifest in the cache.
func Resolve(c *blob.DiskCache, name string) (*Manifest, error) {
	d, err := c.Resolve(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(c.GetFile(d))
	if err != nil {
		return nil, err
	}
	return unmarshalManifest(name, data)
}

// Resolve resolves a name to a Manifest in the remote registry.
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
	return unmarshalManifest(name, data)
}

func (r *Registry) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

// newRequest constructs a new request, ready to use, with the given method,
// path, and body, presigned with client Key and UserAgent.
//
// If the path is relative, it is prefixed with the registry's BaseURL, or
// [DefaultRegistryURL] if the BaseURL is empty.
func (r *Registry) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if strings.HasPrefix(path, "/") {
		// If the path is relative, prepend the registry's BaseURL.
		baseURL := r.BaseURL
		if baseURL == "" {
			baseURL = DefaultRegistryURL
		}
		path = baseURL + path
	}
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if r.UserAgent != "" {
		req.Header.Set("User-Agent", r.UserAgent)
	}
	token, err := makeAuthToken(r.Key)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// doOK makes a request with the given client and request, and returns the
// response if the status code is 200. If the status code is not 200, an Error
// is parsed from the response body and returned. If any other error occurs, it
// is returned.
func doOK(c *http.Client, r *http.Request) (*http.Response, error) {
	res, err := c.Do(r)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
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

// doOK is a convenience method for making a request with newRequest and
// passing it to doOK with r.client().
func (r *Registry) doOK(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := r.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return doOK(r.client(), req)
}

func splitNameTagDigest(s string) (shortName, tag, digest string) {
	// TODO(bmizerany): fix this hot mess
	s, digest, _ = strings.Cut(s, "@")
	n := names.Parse(s)
	return n.Namespace() + "/" + n.Model(), n.Tag(), digest
}

func makeBlobPath(name string, d blob.Digest) string {
	shortName, _, _ := splitNameTagDigest(name)
	return "/v2/" + shortName + "/blobs/" + d.String()
}

func makeAuthToken(key crypto.PrivateKey) (string, error) {
	privKey, _ := key.(*ed25519.PrivateKey)
	if privKey == nil {
		return "", fmt.Errorf("unsupported private key type: %T", key)
	}

	url := fmt.Sprintf("https://ollama.com?ts=%d", time.Now().Unix())
	// Part 1: the checkData (e.g. the URL with a timestamp)

	// Part 2: the public key\
	pubKeyShort, err := func() ([]byte, error) {
		sshPubKey, err := ssh.NewPublicKey(privKey.Public())
		if err != nil {
			return nil, err
		}
		pubKeyParts := bytes.Fields(ssh.MarshalAuthorizedKey(sshPubKey))
		if len(pubKeyParts) < 2 {
			return nil, fmt.Errorf("malformed public key: %q", pubKeyParts)
		}
		pubKeyShort := pubKeyParts[1]
		return pubKeyShort, nil
	}()
	if err != nil {
		return "", err
	}

	// Part 3: the signature
	sig := ed25519.Sign(*privKey, []byte(checkData(url)))

	// Assemble the token: <checkData>:<pubKey>:<signature>
	var b strings.Builder
	io.WriteString(&b, base64.StdEncoding.EncodeToString([]byte(url)))
	b.WriteByte(':')
	b.Write(pubKeyShort)
	b.WriteByte(':')
	io.WriteString(&b, base64.StdEncoding.EncodeToString(sig))

	return b.String(), nil
}

// The original spec for Ollama tokens was to use the SHA256 of the zero
// string as part of the signature. I'm not sure why that was, but we still
// need it to verify the signature.
var zeroSum = func() string {
	sha256sum := sha256.Sum256(nil)
	x := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(sha256sum[:])))
	return x
}()

// checkData takes a url and creates the original string format of the
// data signature that is used by the ollama client to sign requests
func checkData(url string) string {
	return fmt.Sprintf("GET,%s,%s", url, zeroSum)
}
