package main

import (
	"context"
	"log"

	"github.com/bmizerany/ollama-go/client/ollama"
)

func main() {
	ctx := context.Background()

	var rc ollama.Registry
	for l, err := range rc.Layers(ctx, "library/llama3.2:latest") {
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("layer: %v", l)
	}

	c, err := ollama.DefaultCache()
	if err != nil {
		log.Fatal(err)
	}
	if err := rc.Pull(ctx, c, "library/llama3.2:latest"); err != nil {
		log.Fatal(err)
	}
}
