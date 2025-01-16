package main

import (
	"context"
	"log"

	"github.com/bmizerany/ollama-go/client/ollama"
)

func main() {
	var rc ollama.Registry

	ll, err := rc.Layers(context.Background(), "library/llama3.2:latest")
	if err != nil {
		log.Fatal(err)
	}
	for _, l := range ll {
		log.Printf("layer: %v", l)
	}
}
