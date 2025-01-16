package main

import (
	"context"
	"log"

	"github.com/bmizerany/ollama-go/client/ollama"
)

func main() {
	var rc ollama.Registry
	c, err := ollama.DefaultCache()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := rc.Pull(ctx, c, "library/llama3.2:latest"); err != nil {
		log.Fatal(err)
	}
}
