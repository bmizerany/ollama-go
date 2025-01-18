package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/client/ollama"
)

func main() {
	flag.Parse()

	var rc ollama.Registry
	c, err := ollama.DefaultCache()
	if err != nil {
		log.Fatal(err)
	}

	model := flag.Arg(0)
	if model == "" {
		fmt.Fprintln(os.Stderr, "Usage: opp <model>")
		os.Exit(1)
	}

	ctx := ollama.WithTrace(context.Background(), &ollama.Trace{
		Resolved: func(name string, d blob.Digest) {
			fmt.Printf("Resolved    %s to %s\n", name, d.Short())
		},
		DownloadUpdate: func(d blob.Digest, n int64, size int64, err error) {
			fmt.Printf("Downloading %s: %d/%d bytes\n", d.Short(), n, size)
		},
	})
	if err := rc.Pull(ctx, c, model); err != nil {
		log.Fatal(err)
	}
}
