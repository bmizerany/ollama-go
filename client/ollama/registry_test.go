package ollama

import (
	"bytes"
	"encoding/json"
	"testing"
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
