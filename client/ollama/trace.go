package ollama

import "github.com/bmizerany/ollama-go/blob"

type Trace struct {
	DownloadUpdate func(d blob.Digest, n, size int64) error
}
