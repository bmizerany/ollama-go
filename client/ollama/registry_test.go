package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama.com/cache/blob"
	"ollama.com/internal/testutil"
)

func TestManifestMarshalJSON(t *testing.T) {
	// All manfiests should contain an "empty" config object.
	var m Manifest
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"config":{"digest":"sha256:`)) {
		t.Error("expected manifest to contain empty config")
		t.Fatalf("got:\n%s", string(data))
	}
}

func link(c *blob.DiskCache, name string, manifest string) {
	_, n, _, err := parseName(name)
	if err != nil {
		panic(err)
	}
	d, err := c.Import(bytes.NewReader([]byte(manifest)), int64(len(manifest)))
	if err != nil {
		panic(err)
	}
	if err := c.Link(n.String(), d); err != nil {
		panic(err)
	}
}

type recordRoundTripper http.HandlerFunc

func (rr recordRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	rr(w, req)
	return w.Result(), nil
}

// newClient constructs a cache with a single image named "empty" and a
// registry client that will use the provided handler as the upstream registry.
//
// Tests that want to ensure the client does not communicate with the upstream
// registry should pass a nil handler, which will cause a panic if
// communication is attempted.
func newClient(t *testing.T, h http.HandlerFunc) (*Registry, *blob.DiskCache) {
	t.Helper()
	c, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	link(c, "empty", `{}`)

	rc := &Registry{HTTPClient: &http.Client{Transport: recordRoundTripper(h)}}
	return rc, c
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func checkErrCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Errorf("err = %v; want %v", err, code)
	}
}

func importBytes(t *testing.T, c *blob.DiskCache, data string) blob.Digest {
	d, err := c.Import(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRegistryPush(t *testing.T) {
	t.Run("invalid names", func(t *testing.T) {
		rc, c := newClient(t, nil)

		cases := []struct {
			name string
			err  error
		}{
			{"", ErrInvalidName},
			{"@", ErrInvalidName},
			{"@x", blob.ErrInvalidDigest},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				// Create a new registry and push a new image.
				err := rc.Push(t.Context(), c, tt.name, nil)
				if !errors.Is(err, tt.err) {
					t.Errorf("err = %v; want %v", err, tt.err)
				}
			})
		}
	})

	t.Run("empty image", func(t *testing.T) {
		rc, c := newClient(t, okHandler)
		err := rc.Push(t.Context(), c, "empty", nil)
		testutil.Check(t, err)
	})

	t.Run("with layer", func(t *testing.T) {
		check := testutil.Checker(t)
		rc, c := newClient(t, okHandler)

		d := importBytes(t, c, "hello")
		link(c, "empty", fmt.Sprintf(`{"layers":[{"digest":%q}]}`, d))

		// Create a new registry and push a new image.
		err := rc.Push(t.Context(), c, "empty", nil)
		check(err)
	})

	t.Run("layer not found", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://example.com/upload/123")
			w.WriteHeader(http.StatusAccepted)
		})
		d := blob.DigestFromBytes("hello") // not put in cache
		link(c, "x", fmt.Sprintf(`{"layers":[{"digest":%q}]}`, d))

		// Create a new registry and push a new image.
		err := rc.Push(t.Context(), c, "x", nil)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v; want %v", err, fs.ErrNotExist)
		}
	})

	t.Run("layer upload error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "blobs") {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"errors":[{"code":"blob_error"}]}`)
				return
			}
		})

		d := importBytes(t, c, "hello")
		link(c, "x", fmt.Sprintf(`{"layers":[{"digest":%q}]}`, d))

		err := rc.Push(t.Context(), c, "x", nil)
		checkErrCode(t, err, "blob_error")
	})

	t.Run("manifest upload error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "manifests") {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"errors":[{"code":"manifest_error"}]}`)
				return
			}

			// Otherwise, return a layer upload URL.
			w.Header().Set("Location", "http://example.com/upload/123")
			w.WriteHeader(http.StatusAccepted)
		})

		d := importBytes(t, c, "hello")
		link(c, "x", fmt.Sprintf(`{"layers":[{"digest":%q}]}`, d))

		// Create a new registry and push a new image.
		err := rc.Push(t.Context(), c, "x", nil)
		checkErrCode(t, err, "manifest_error")
	})
}
