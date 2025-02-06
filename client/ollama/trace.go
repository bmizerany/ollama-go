package ollama

import (
	"context"
	"errors"
	"io"

	"ollama.com/cache/blob"
)

// Trace is a set of functions that are called to report progress during blob
// downloads and uploads.
type Trace struct {
	// PullUpdate is called during Pull to report the progress of blob
	// downloads.
	//
	// It is called once at the beginning of the download with n=0, and per
	// read while 0 < n < size, and again when n=size.
	//
	// If an error occurred during the download, d, n, and size will be
	// their values the time of the error, and err will be non-nil.
	//
	// A function assigned must be safe for concurrent use. The function is
	// called synchronously and so should not block or take long to run.
	//
	// If it panics, all downloads will be aborted.
	PullUpdate func(_ blob.Digest, n, size int64, _ error)

	// PushUpdate is like PullUpdate, but for blob uploads during Push.
	PushUpdate func(_ blob.Digest, n, size int64, _ error)
}

func (t *Trace) pullUpdate(d blob.Digest, n, size int64, err error) {
	if t.PullUpdate != nil {
		t.PullUpdate(d, n, size, err)
	}
}

func (t *Trace) pushUpdate(d blob.Digest, n, size int64, err error) {
	if t.PushUpdate != nil {
		t.PushUpdate(d, n, size, err)
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
	l  *Layer
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
