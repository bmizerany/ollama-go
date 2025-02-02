// Package ollama provides a client for interacting with Ollama registries and
// a local cache for storing models.
//
// # Names
//
// The client supports pushing and pulling models by name. The name of a model
// is a string that identifies the model in the registry. The name is composed
// of five parts, three of which are optional:
//
//	{host/}{namespace/}[model]{:tag}{@digest}
//
// The above shows the required separator characters for the optional parts
// when present.
//
// The default host is "ollama.com"; The default namespace is "library"; The
// default tag is "latest". There is no default digest.
//
// When a digest is present, the name is ignored.
//
// As a special case, the name may be in URL form. If the name starts with
// "http://", "https://", or "https+insecure://", the host is the URL and the
// rest of the name is the model name. When "https+insecure://" is used, the
// client uses TLS but does not verify the server's certificate. Names not in
// URL form use "https".
package ollama

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
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

	// ErrMissingModel is returned when the model part of a name is missing
	// or invalid.
	ErrInvalidName = errors.New("invalid name; must be in the form {host/}{namespace/}[model]{:tag}{@digest}")
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

// TODO(bmizerany): make configurable on [Registry]
var defaultName = func() names.Name {
	n := names.Parse("ollama.com/library/_:latest")
	if !n.IsFullyQualified() {
		panic("default name is not fully qualified")
	}
	return n
}()

// Registry is a client for performing push and pull operations against an
// Ollama registry.
type Registry struct {
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
	//
	// The https+insecure scheme is supported only if the client's
	// Transport implements a clone function with the same signature as
	// http.Transport.Clone
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
	rc := &Registry{Key: key}
	return rc, nil
}

type PushParams struct {
	// From is an optional destination name for the model. If empty, the
	// destination name is the same as the source name.
	From string
}

// parseName parses a name using [names.ParseExtended] and returns the scheme,
// parsed name, and parsed digest, if present.
//
// The strings must contain a valid Name, or an error is returned. If a digest is present,
// it is parsed with [blob.ParseDigest].
//
// The scheme is the scheme part of the name, or "https" if not present.
func parseName(s string) (scheme string, n names.Name, d blob.Digest, err error) {
	scheme, n, ds := names.ParseExtended(s)
	n = names.Merge(n, defaultName)
	if !n.IsFullyQualified() {
		return "", names.Name{}, blob.Digest{}, ErrInvalidName
	}
	if ds != "" {
		// Digest is present. Validate it.
		d, err = blob.ParseDigest(ds)
		if err != nil {
			return "", names.Name{}, blob.Digest{}, err
		}
	}
	return scheme, n, d, nil
}

// Push pushes the model with the name in the cache to the remote registry.
func (r *Registry) Push(ctx context.Context, c *blob.DiskCache, name string, p *PushParams) error {
	if p == nil {
		p = &PushParams{}
	}

	m, err := ResolveLocal(c, cmp.Or(p.From, name))
	if err != nil {
		return err
	}

	t := traceFromContext(ctx)

	scheme, n, _, err := parseName(name)
	if err != nil {
		return err
	}

	// TODO(bmizerany): backoff and retry with resumable uploads (need to
	// ask server how far along we are)
	upload := func(l *Layer) error {
		t.pushUpdate(l.Digest, 0, l.Size, nil) // initial update

		startURL := fmt.Sprintf("%s://%s/v2/%s/%s/blobs/uploads/?digest=%s",
			scheme,
			n.Host(),
			n.Namespace(),
			n.Model(),
			l.Digest,
		)
		res, err := r.doOK(ctx, "POST", startURL, nil)
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
			if errors.Is(err, http.ErrNoLocation) {
				return nil // already uploaded
			}
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
			return err
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
		g.Go(func() error {
			if err := upload(l); err != nil {
				return fmt.Errorf("error uploading %s: %w", l.Digest.Short(), err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	path := fmt.Sprintf("%s://%s/v2/%s/%s/manifests/%s",
		scheme,
		n.Host(),
		n.Namespace(),
		n.Model(),
		n.Tag(),
	)
	res, err := r.doOK(ctx, "PUT", path, bytes.NewReader(m.Data))
	if err == nil {
		res.Body.Close()
	}
	return err
}

// Pull pulls the model with the given name from the remote registry into the
// cache.
func (r *Registry) Pull(ctx context.Context, c *blob.DiskCache, name string) error {
	t := traceFromContext(ctx)

	scheme, n, _, err := parseName(name)
	if err != nil {
		return err
	}

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

		blobURL := fmt.Sprintf("%s://%s/v2/%s/%s/blobs/%s", scheme, n.Host(), n.Namespace(), n.Model(), l.Digest)
		res, err := r.doOK(ctx, "GET", blobURL, nil)
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
		return errors.New("invalid manifest; no layers")
	}

	// download all the layers and put them in the cache
	g := syncs.NewGroup(r.MaxStreams)
	for _, l := range m.Layers {
		g.Go(func() error {
			err := download(l)
			if err != nil {
				return fmt.Errorf("error uploading %s: %w", l.Digest.Short(), err)
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

// Layer returns the layer with the given
// digest, or nil if not found.
func (m Manifest) Layer(d blob.Digest) *Layer {
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

func unmarshalManifest(n names.Name, data []byte) (*Manifest, error) {
	if !n.IsFullyQualified() {
		panic(fmt.Sprintf("unmarshalManifest: name is not fully qualified: %s", n.String()))
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m.Name = n.String()
	m.Data = data
	return &m, nil
}

// Layer is a layer in a model.
type Layer struct {
	Digest    blob.Digest `json:"digest"`
	MediaType string      `json:"mediaType"`
	Size      int64       `json:"size"`
}

// ResolveLocal resolves a name to a Manifest in the local cache. The name may
// be in [names.ParseExtended] form, but the scheme will be ignored.
func ResolveLocal(c *blob.DiskCache, name string) (*Manifest, error) {
	_, n, d, err := parseName(name)
	if err != nil {
		return nil, err
	}
	if !d.IsValid() {
		d, err = c.Resolve(n.String())
		if err != nil {
			return nil, err
		}
	}
	_, err = c.Get(d)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrManifestNotFound, name)
	}
	data, err := os.ReadFile(c.GetFile(d))
	if err != nil {
		return nil, err
	}
	return unmarshalManifest(n, data)
}

// Resolve resolves a name to a Manifest in the remote registry.
func (r *Registry) Resolve(ctx context.Context, name string) (*Manifest, error) {
	scheme, n, d, err := parseName(name)
	if err != nil {
		return nil, err
	}

	manifestURL := fmt.Sprintf("%s://%s/v2/%s/%s/manifests/%s", scheme, n.Host(), n.Namespace(), n.Model(), n.Tag())
	if d.IsValid() {
		manifestURL = fmt.Sprintf("%s://%s/v2/%s/%s/blobs/%s", scheme, n.Host(), n.Namespace(), n.Model(), d)
	}

	res, err := r.doOK(ctx, "GET", manifestURL, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return unmarshalManifest(n, data)
}

func (r *Registry) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

// newRequest constructs a new request, ready to use, with the given method,
// url, and body, presigned with client Key and UserAgent.
func (r *Registry) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
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
	if r.URL.Scheme == "https+insecure" {
		// TODO(bmizerany): clone client.Transport, set
		// InsecureSkipVerify, etc.

		type cloner interface {
			Clone() *http.Transport
		}

		// Panics if c.Transport is not an cloner (if this
		// ever becomes not the case, then there should be tests that
		// caught this and modified it accordingly).
		x, ok := cmp.Or(c.Transport, http.DefaultTransport).(cloner)
		if !ok {
			// This should never happen if we're testing properly.
			return nil, fmt.Errorf("cannot use set http transport with https+insecure: %T", c.Transport)
		}
		tr := x.Clone()
		tr.TLSClientConfig = cmp.Or(tr.TLSClientConfig, &tls.Config{})
		tr.TLSClientConfig.InsecureSkipVerify = true

		cc := *c // shallow copy
		cc.Transport = tr
		c = &cc

		r = r.Clone(r.Context())
		r.URL.Scheme = "https"
	}

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
			// Use the raw body if we can't parse it as an error object.
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

// makeAuthToken creates an Ollama auth token for the given private key.
//
// NOTE: This format is OLD, overly complex, and should be replaced. We're
// inheriting it from the original Ollama client and ollama.com
// implementations, so we need to support it for now.
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
