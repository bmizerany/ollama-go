package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/trace"
	"sync"
	"time"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/client/ollama"

	"rsc.io/omap"
)

var stdout io.Writer = os.Stdout

const usage = `Opp is a tool for pushing and pulling Ollama models.

Usage:

    opp [flags] <push|pull|import>

Commands:

    push    Upload a model to the Ollama server.
    pull    Download a model from the Ollama server.
    import  Import a model from a local safetensor directory.

Examples:

    # Pull a model from the Ollama server.
    opp pull library/llama3.2:latest

    # Push a model to the Ollama server.
    opp push username/my_model:8b 

    # Import a model from a local safetensor directory.
    opp import /path/to/safetensor

Envionment Variables:

    OLLAMA_MODELS
        The directory where models are pushed and pulled from
	(default ~/.ollama/models).
`

func main() {
	flagTrace := flag.String("trace", "", "Write an execution trace to the specified file before exiting.")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, usage)
	}
	flag.Parse()

	if *flagTrace != "" {
		defer doTrace(*flagTrace)()
	}

	c, err := ollama.DefaultCache()
	if err != nil {
		log.Fatal(err)
	}

	var rc ollama.Registry
	err = func() error {
		switch cmd := flag.Arg(0); cmd {
		case "push":
			return cmdPull(&rc, c)
		case "pull":
			return cmdPull(&rc, c)
		case "import":
			return cmdImport(&rc, c)
		default:
			if cmd == "" {
				flag.Usage()
			} else {
				fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
			}
			os.Exit(2)
			return errors.New("unreachable")
		}
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

}

func cmdPull(rc *ollama.Registry, c *blob.DiskCache) error {
	model := flag.Arg(1)
	if model == "" {
		flag.Usage()
		os.Exit(1)
	}

	csiHideCursor(stdout)
	defer csiShowCursor(stdout)

	ctx := context.Background()

	var pmu sync.Mutex
	var states []state
	progress := omap.NewMapFunc[blob.Digest, state](blob.Digest.Compare)

	done := make(chan error)
	go func() {
		ctx := ollama.WithTrace(ctx, &ollama.Trace{
			DownloadUpdate: func(d blob.Digest, n int64, size int64, err error) {
				pmu.Lock()
				progress.Set(d, state{d: d, n: n, size: size, err: err})
				pmu.Unlock()
			},
		})

		done <- rc.Pull(ctx, c, model)
	}()

	fmt.Fprintln(stdout, "Downloading...")
	csiSavePos(stdout) // for redrawing progress bars

	tt := time.NewTicker(time.Second)
	getStates := func() []state {
		pmu.Lock()
		defer pmu.Unlock()
		states = states[:0]
		for _, s := range progress.All() {
			states = append(states, s)
		}
		return states
	}

	for {
		csiRestorePos(stdout)

		select {
		case <-tt.C:
			writeProgress(stdout, getStates())
		case err := <-done:
			writeProgress(stdout, getStates())
			if err != nil {
				log.Fatal(err)
			}
			return nil
		}

	}
}

func cmdPush(rc *ollama.Registry, c *blob.DiskCache) error {
	panic("TODO")
}

func cmdImport(rc *ollama.Registry, c *blob.DiskCache) error {
	panic("TODO")
}

type state struct {
	d       blob.Digest
	n, size int64
	err     error
}

// writeProgress draws progress bars for each blob being downloaded. The
// terminal codes are used to update redraw from the start position. If the
// start position is 0, the current position is used and returned for the next
// call.
func writeProgress(w io.Writer, p []state) {
	for _, s := range p {
		if s.err != nil {
			fmt.Fprintf(w, "%s %3d%% %v/%v ! %v\n",
				s.d.Short(),
				s.n*100/s.size,
				FormatBytes(s.n),
				FormatBytes(s.size),
				s.err)
		} else {
			fmt.Fprintf(w, "%s %3d%% %v/%v\n",
				s.d.Short(),
				s.n*100/s.size,
				FormatBytes(s.n),
				FormatBytes(s.size))
		}
		csiClearLine(w)
	}
}

func FormatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
	}
}

func csiSavePos(w io.Writer)    { fmt.Fprint(w, "\033[s") }
func csiRestorePos(w io.Writer) { fmt.Fprint(w, "\033[u") }
func csiHideCursor(w io.Writer) { fmt.Fprint(w, "\033[?25l") }
func csiShowCursor(w io.Writer) { fmt.Fprint(w, "\033[?25h") }
func csiClearLine(w io.Writer)  { fmt.Fprint(w, "\033[K") }
func csiMoveToEnd(w io.Writer)  { fmt.Fprint(w, "\033[1000D") }

func doTrace(filename string) func() {
	f, err := os.Create(filename)
	if err != nil {
		log.Fatalf("failed to create trace output file: %v", err)
	}
	if err := trace.Start(f); err != nil {
		log.Printf("failed to start trace: %v", err)
	}
	return func() {
		trace.Stop()
		if err := f.Close(); err != nil {
			log.Fatalf("failed to close trace file: %v", err)
		}
	}
}
