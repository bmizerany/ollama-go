package ollama

// func TestLayers(t *testing.T) {
// 	dir := t.TempDir()
// 	chdir(t, dir)

// 	// Create a manifest file.
// 	writeFile(t, "manifests/alice/palace/latest", `{
// 		"layers": [
// 			{"digest": "sha256:1234", "size": 100, "mediaType": "application/vnd.ollama.image.layer"},
// 			{"digest": "sha256:5678", "size": 200, "mediaType": "application/vnd.ollama.image.layer"}
// 		]
// 	}`)

// 	c := &cache.Disk{Dir: dir}
// 	r := &Registry{Cache: c}

// 	var got []Layer
// 	for _, l := range r.Layers("alice/palace") {
// 		got = append(got, l)
// 	}
// }
