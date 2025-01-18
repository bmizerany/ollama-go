package ollama

import (
	"context"

	"github.com/bmizerany/ollama-go/blob"
)

type Trace struct {
	// DownloadUpdate periodically reports the progress of a blob download.
	// The digest d is being downloaded, n bytes have been downloaded so
	// far, and the total size of the blob is size bytes.
	//
	// If an error occurred during the download, d, n, and size will be
	// their current values, and err will be non-nil.
	DownloadUpdate func(d blob.Digest, n, size int64, err error)
}

type traceKey struct{}

// WithTrace returns a context derived from ctx that uses t to report trace
// events.
func WithTrace(ctx context.Context, t *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

var emptyTrace = &Trace{}

func traceFromContext(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	if t == nil {
		return emptyTrace
	}
	return t
}
