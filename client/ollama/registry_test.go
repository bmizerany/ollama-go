package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
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

var errRoundTrip = errors.New("forced roundtrip error")

type recordRoundTripper http.HandlerFunc

func (rr recordRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	rr(w, req)
	if w.Code == 999 {
		return nil, errRoundTrip
	}
	return w.Result(), nil
}

// newClient constructs a cache with predefined manifests for testing. The manifests are:
//
//	empty: no data
//	zero: no layers
//	single: one layer with the contents "exists"
//	multiple: two layers with the contents "exists" and "here"
//	notfound: a layer that does not exist in the cache
//	null: one null layer (e.g. [null])
//	sizemismatch: one valid layer, and one with a size mismatch (file size is less than the reported size)
//	invalid: a layer with invalid JSON data
//
// Tests that want to ensure the client does not communicate with the upstream
// registry should pass a nil handler, which will cause a panic if
// communication is attempted.
//
// To simulate a network error, pass a handler that returns a 999 status code.
func newClient(t *testing.T, h http.HandlerFunc) (*Registry, *blob.DiskCache) {
	t.Helper()
	c, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	mklayer := func(data string) *Layer {
		return &Layer{
			Digest: importBytes(t, c, data),
			Size:   int64(len(data)),
		}
	}

	commit := func(name string, layers ...*Layer) {
		t.Helper()
		data, err := json.Marshal(&Manifest{Layers: layers})
		if err != nil {
			t.Fatal(err)
		}
		link(c, name, string(data))
	}

	link(c, "empty", "")
	commit("zero")
	commit("single", mklayer("exists"))
	commit("multiple", mklayer("exists"), mklayer("present"))
	commit("notfound", &Layer{Digest: blob.DigestFromBytes("notfound"), Size: int64(len("notfound"))})
	commit("null", nil)
	commit("sizemismatch", mklayer("exists"), &Layer{Digest: blob.DigestFromBytes("present"), Size: 999})
	link(c, "invalid", "!!!!!")

	rc := &Registry{HTTPClient: &http.Client{
		Transport: recordRoundTripper(h),
	}}
	return rc, c
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func checkErrCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Errorf("err = %v; want %v %v", err, status, code)
	}
}

func importBytes(t *testing.T, c *blob.DiskCache, data string) blob.Digest {
	d, err := c.Import(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRegistryPushInvalidNames(t *testing.T) {
	rc, c := newClient(t, nil)

	cases := []struct {
		name string
		err  error
	}{
		{"", ErrNameInvalid},
		{"@", ErrNameInvalid},
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
}

func withTraceUnexpected(ctx context.Context) (context.Context, *Trace) {
	t := &Trace{
		PullUpdate: func(blob.Digest, int64, int64, error) { panic("unexpected") },
		PushUpdate: func(blob.Digest, int64, int64, error) { panic("unexpected") },
	}
	return WithTrace(ctx, t), t
}

func TestRegistryPush(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		rc, c := newClient(t, okHandler)
		err := rc.Push(t.Context(), c, "empty", nil)
		if !errors.Is(err, ErrManifestInvalid) {
			t.Errorf("err = %v; want %v", err, ErrManifestInvalid)
		}
	})

	t.Run("zero", func(t *testing.T) {
		rc, c := newClient(t, okHandler)
		err := rc.Push(t.Context(), c, "empty", nil)
		if !errors.Is(err, ErrManifestInvalid) {
			t.Errorf("err = %v; want %v", err, ErrManifestInvalid)
		}
	})

	t.Run("single", func(t *testing.T) {
		rc, c := newClient(t, okHandler)
		err := rc.Push(t.Context(), c, "single", nil)
		testutil.Check(t, err)
	})

	t.Run("multiple", func(t *testing.T) {
		rc, c := newClient(t, okHandler)
		err := rc.Push(t.Context(), c, "multiple", nil)
		testutil.Check(t, err)
	})

	t.Run("notfound", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request: %v", r)
		})
		err := rc.Push(t.Context(), c, "notfound", nil)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v; want %v", err, fs.ErrNotExist)
		}
	})

	t.Run("null layer", func(t *testing.T) {
		rc, c := newClient(t, nil)
		err := rc.Push(t.Context(), c, "null", nil)
		if err == nil || !strings.Contains(err.Error(), "invalid manifest") {
			t.Errorf("err = %v; want invalid manifest", err)
		}
	})

	t.Run("sizemismatch", func(t *testing.T) {
		rc, c := newClient(t, nil)
		ctx, _ := withTraceUnexpected(t.Context())
		got := rc.Push(ctx, c, "sizemismatch", nil)
		if got == nil || !strings.Contains(got.Error(), "size mismatch") {
			t.Errorf("err = %v; want size mismatch", got)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		rc, c := newClient(t, nil)
		err := rc.Push(t.Context(), c, "invalid", nil)
		if err == nil || !strings.Contains(err.Error(), "invalid manifest") {
			t.Errorf("err = %v; want invalid manifest", err)
		}
	})

	t.Run("exists at remote", func(t *testing.T) {
		var pushed bool
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/uploads/") {
				if !pushed {
					// First push. Return an uploadURL.
					pushed = true
					w.Header().Set("Location", "http://blob.store/blobs/123")
					return
				}
				w.WriteHeader(http.StatusAccepted)
				return
			}

			io.Copy(io.Discard, r.Body)

			w.WriteHeader(http.StatusOK)
		})

		rc.MaxStreams = 1 // prevent concurrent uploads

		var errs []error
		ctx := WithTrace(t.Context(), &Trace{
			PullUpdate: func(blob.Digest, int64, int64, error) { panic("unexpected") },
			PushUpdate: func(g blob.Digest, n, size int64, err error) {
				// uploading one at a time so no need to lock
				errs = append(errs, err)
			},
		})

		check := testutil.Checker(t)

		err := rc.Push(ctx, c, "single", nil)
		check(err)

		if !errors.Is(errors.Join(errs...), nil) {
			t.Errorf("errs = %v; want %v", errs, []error{ErrLayerExists})
		}

		err = rc.Push(ctx, c, "single", nil)
		check(err)
	})

	t.Run("remote error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/blobs/") {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"errors":[{"code":"blob_error"}]}`)
				return
			}
		})
		err := rc.Push(t.Context(), c, "single", nil)
		checkErrCode(t, err, 500, "blob_error")
	})

	t.Run("Location error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", ":///x")
			w.WriteHeader(http.StatusAccepted)
		})
		err := rc.Push(t.Context(), c, "single", nil)
		wantContains := "invalid upload URL"
		if !strings.Contains(err.Error(), wantContains) {
			t.Errorf("err = %v; want to contain %v", err, wantContains)
		}
	})

	t.Run("upload roundtrip error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Host == "blob.store" {
				w.WriteHeader(999) // force RoundTrip error on upload
				return
			}
			w.Header().Set("Location", "http://blob.store/blobs/123")
		})
		err := rc.Push(t.Context(), c, "single", nil)
		if !errors.Is(err, errRoundTrip) {
			t.Errorf("err = %v; want %v", err, errRoundTrip)
		}
	})

	t.Run("upload file open error", func(t *testing.T) {
		rc, c := newClient(t, okHandler)
		ctx, tv := withTraceUnexpected(t.Context())
		tv.PushUpdate = func(d blob.Digest, _, _ int64, _ error) {
			// Remove the file just before it is opened for upload,
			// but after the initial Stats before upload goroutines
			// start.
			os.Remove(c.GetFile(d))
		}
		err := rc.Push(ctx, c, "single", nil)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v; want fs.ErrNotExist", err)
		}
	})

	t.Run("commit roundtrip error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/blobs/") {
				panic("unexpected")
			}
			w.WriteHeader(999) // force RoundTrip error
		})
		err := rc.Push(t.Context(), c, "zero", nil)
		if !errors.Is(err, errRoundTrip) {
			t.Errorf("err = %v; want %v", err, errRoundTrip)
		}
	})
}

// TestPushLayerUploadGoroutinePanic tests that a panic during a layer upload
// is recovered and handed to the calling goroutine to handle.
func TestPushLayerUploadGoroutinePanic(t *testing.T) {
	ctx := WithTrace(t.Context(), &Trace{
		PullUpdate: func(blob.Digest, int64, int64, error) { panic("unexpected") },
		PushUpdate: func(g blob.Digest, n, size int64, err error) { panic("unexpected") },
	})

	rc, c := newClient(t, okHandler)

	testutil.StopPanic(func() {
		err := rc.Push(ctx, c, "single", nil)
		t.Errorf("err = %v; want panic", err)
	})
}

func checkNotExist(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v; want fs.ErrNotExist", err)
	}
}

func TestRegistryPull(t *testing.T) {
	t.Run("invalid name", func(t *testing.T) {
		rc, c := newClient(t, nil)
		err := rc.Pull(t.Context(), c, "://")
		if !errors.Is(err, ErrNameInvalid) {
			t.Errorf("err = %v; want %v", err, ErrNameInvalid)
		}
	})

	t.Run("bad manifest", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "!!!!")
		})
		err := rc.Pull(t.Context(), c, "empty")
		if err == nil || !strings.Contains(err.Error(), "invalid manifest") {
			t.Errorf("err = %v; want invalid manifest", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, ``)
		})
		err := rc.Pull(t.Context(), c, "empty")
		if !errors.Is(err, ErrManifestInvalid) {
			t.Errorf("err = %v; want %v", err, ErrManifestInvalid)
		}
	})

	t.Run("zero", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"layers":[]}`)
		})
		err := rc.Pull(t.Context(), c, "zero")
		if !errors.Is(err, ErrManifestInvalid) {
			t.Errorf("err = %v; want %v", err, ErrManifestInvalid)
		}
	})

	t.Run("unfetched", func(t *testing.T) {
		check := testutil.Checker(t)

		var c *blob.DiskCache
		var rc *Registry

		d := blob.DigestFromBytes("some data")
		rc, c = newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/blobs/") {
				io.WriteString(w, "some data")
				return
			}
			fmt.Fprintf(w, `{"layers":[{"digest":%q,"size":9}]}`, d)
		})

		// Check that the manifest and layer are not in the cache.
		_, err := ResolveLocal(c, "unfetched")
		checkNotExist(t, err)

		_, err = c.Get(d)
		checkNotExist(t, err)

		err = rc.Pull(t.Context(), c, "unfetched")
		check(err)

		mw, err := rc.Resolve(t.Context(), "unfetched")
		check(err)
		mg, err := ResolveLocal(c, "unfetched")
		check(err)
		if !reflect.DeepEqual(mw, mg) {
			t.Errorf("mw = %v; mg = %v", mw, mg)
		}

		// Check that the layer was imported.
		info, err := c.Get(d)
		check(err)
		if info.Digest != d {
			t.Errorf("info.Digest = %v; want %v", info.Digest, d)
		}
		if info.Size != 9 {
			t.Errorf("info.Size = %v; want %v", info.Size, 9)
		}

		data, err := os.ReadFile(c.GetFile(d))
		check(err)
		if string(data) != "some data" {
			t.Errorf("data = %q; want %q", data, "exists")
		}
	})

	t.Run("fetched", func(t *testing.T) {
		existing := blob.DigestFromBytes("exists")
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/blobs/") {
				w.WriteHeader(999) // should not be called
				return
			}
			fmt.Fprintf(w, `{"layers":[{"digest":%q,"size":6}]}`, existing)
		})

		var pairs []int64
		ctx := WithTrace(t.Context(), &Trace{
			PushUpdate: func(blob.Digest, int64, int64, error) { panic("unexpected") },
			PullUpdate: func(_ blob.Digest, n, size int64, err error) {
				if err != nil {
					panic(err)
				}
				pairs = append(pairs, n, size)
			},
		})

		err := rc.Pull(ctx, c, "single")
		testutil.Check(t, err)

		want := []int64{0, 6, 6, 6}
		if !slices.Equal(pairs, want) {
			t.Errorf("pairs = %v; want %v", pairs, want)
		}
	})

	t.Run("resolve manifest not found", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		err := rc.Pull(t.Context(), c, "notfound")
		checkErrCode(t, err, 404, "")
	})

	t.Run("resolve remote error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"errors":[{"code":"an_error"}]}`)
		})
		err := rc.Pull(t.Context(), c, "single")
		checkErrCode(t, err, 500, "an_error")
	})

	t.Run("resolve roundtrip error", func(t *testing.T) {
		rc, c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/manifests/") {
				w.WriteHeader(999) // force RoundTrip error
				return
			}
		})
		err := rc.Pull(t.Context(), c, "single")
		if !errors.Is(err, errRoundTrip) {
			t.Errorf("err = %v; want %v", err, errRoundTrip)
		}
	})
}

func TestRegistryResolveByDigest(t *testing.T) {
	check := testutil.Checker(t)

	exists := blob.DigestFromBytes("exists")
	rc, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/alice/palace/blobs/"+exists.String() {
			w.WriteHeader(999) // should not hit manifest endpoint
		}
		fmt.Fprintf(w, `{"layers":[{"digest":%q,"size":5}]}`, exists)
	})

	_, err := rc.Resolve(t.Context(), "alice/palace@"+exists.String())
	check(err)
}

func TestInsecureSkipVerify(t *testing.T) {
	exists := blob.DigestFromBytes("exists")

	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"layers":[{"digest":%q,"size":5}]}`, exists)
	}))
	defer s.Close()

	const name = "ollama.com/library/insecure"

	var rc Registry
	url := fmt.Sprintf("https://%s/%s", s.Listener.Addr(), name)
	_, err := rc.Resolve(t.Context(), url)
	if err == nil || !strings.Contains(err.Error(), "failed to verify") {
		t.Errorf("err = %v; want cert verifiction failure", err)
	}

	url = fmt.Sprintf("https+insecure://%s/%s", s.Listener.Addr(), name)
	_, err = rc.Resolve(t.Context(), url)
	testutil.Check(t, err)
}
