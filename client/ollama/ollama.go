package ollama

import (
	"context"
	"crypto/ed25519"
	"net/http"

	"github.com/bmizerany/ollama-go/blob"
)

const (
	DefaultRegistryURL = "https://ollama.com"
)

type Registry struct {
	// BaseURL is the base URL of the registry.
	//
	// If empty, DefaultRegistryURL is used.
	BaseURL string

	// Key is the key used to authenticate with the registry.
	Key ed25519.PrivateKey

	// HTTPClient is the HTTP client used to make requests to the registry.
	//
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client
}

type Layer struct {
	Digest    blob.Digest
	Size      int64
	MediaType string
}

// Layers returns the layers of the model with the given name. If the model is
// not found, it returns an error.
//
// The returned layers are in the order they appear in the model's manifest.
func (r *Registry) Layers(ctx context.Context, name string) ([]Layer, error) {
	panic("TODO") // remote fetch only; no cache consideration
}
