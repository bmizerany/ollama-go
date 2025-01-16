package main

import (
	"context"
	"log"

	"github.com/bmizerany/ollama-go/client/ollama"
)

func main() {
	var rc ollama.Registry
	for l, err := range rc.Layers(context.Background(), "library/llama3.2:latest") {
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("layer: %v", l)
	}
}
