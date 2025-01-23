package ollama

import (
	"context"
	"errors"
	"io"

	"github.com/bmizerany/ollama-go/blob"
)

type Trace struct {
	// Resolved is called by Pull when a name is resolved to a digest.
	Resolved func(name string, d blob.Digest)

	// DownloadUpdate is called by Pull to periodically report the progress
	// of a blob download. The digest d is being downloaded, n bytes have
	// been downloaded so far, and the total size of the blob is size
	// bytes.
	//
	// If an error occurred during the download, d, n, and size will be
	// their current values, and err will be non-nil.
	DownloadUpdate func(d blob.Digest, n, size int64, err error)

	// UploadUpdate is called by Push to periodically report the progress
	// of a blob upload. The digest d is being uploaded, n bytes have been
	// uploaded so far, and the total size of the blob is size bytes.
	//
	// If an error occurred during the upload, d, n, and size will be their
	// current values, and err will be non-nil.
	UploadUpdate func(d blob.Digest, n, size int64, err error)
}

func (t *Trace) downloadUpdate(d blob.Digest, n, size int64, err error) {
	if t.DownloadUpdate != nil {
		t.DownloadUpdate(d, n, size, err)
	}
}

func (t *Trace) uploadUpdate(d blob.Digest, n, size int64, err error) {
	if t.UploadUpdate != nil {
		t.UploadUpdate(d, n, size, err)
	}
}

type traceKey struct{}

// WithTrace returns a context derived from ctx that uses t to report trace
// events.
func WithTrace(ctx context.Context, t *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

var emptyTrace = &Trace{}

// traceFromContext returns the Trace associated with ctx, or an empty Trace if
// none is found.
//
// It never returns nil.
func traceFromContext(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	if t == nil {
		return emptyTrace
	}
	return t
}

// traceReader is an io.Reader that reports progress to its fn function.
type traceReader struct {
	l  Layer
	r  io.Reader
	n  int64
	fn func(_ blob.Digest, n int64, size int64, _ error)
}

func (tr *traceReader) Read(p []byte) (n int, err error) {
	n, err = tr.r.Read(p)
	tr.n += int64(n)
	terr := err
	if errors.Is(err, io.EOF) {
		terr = nil
	}
	tr.fn(tr.l.Digest, tr.n, tr.l.Size, terr)
	return
}
