package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"sync"
	"time"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/client/ollama"
	"golang.org/x/crypto/ssh"

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

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

func main() {
	flagTrace := flag.String("trace", "", "Write an execution trace to the specified file before exiting.")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
	}
	flag.Parse()

	if *flagTrace != "" {
		defer doTrace(*flagTrace)()
	}

	c, err := ollama.DefaultCache()
	if err != nil {
		log.Fatal(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		die("failed to get home directory: %v\n", err)
	}

	// TODO(bmizerany): make configurable
	keyPEM, err := os.ReadFile(filepath.Join(home, ".ollama/id_ed25519"))
	if err != nil {
		die("failed to read public key: %v\n", err)
	}

	key, err := ssh.ParseRawPrivateKey(keyPEM)
	if err != nil {
		die("failed to parse private key: %v\n", err)
	}
	rc := ollama.Registry{Key: key}

	err = func() error {
		switch cmd := flag.Arg(0); cmd {
		case "pull":
			return cmdPull(&rc, c)
		case "push":
			return cmdPush(&rc, c)
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

func withProgress(w io.Writer, f func(ctx context.Context) error) error {
	ctx := context.Background()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	csiHideCursor(w)
	defer csiShowCursor(w)

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
			UploadUpdate: func(d blob.Digest, n int64, size int64, err error) {
				pmu.Lock()
				progress.Set(d, state{d: d, n: n, size: size, err: err})
				pmu.Unlock()
			},
		})

		done <- f(ctx)
	}()

	csiSavePos(w) // for redrawing progress bars

	tt := time.NewTicker(time.Second)
	collectStates := func() []state {
		pmu.Lock()
		defer pmu.Unlock()
		states = states[:0]
		for _, s := range progress.All() {
			states = append(states, s)
		}
		return states
	}

	for {
		csiRestorePos(w)

		select {
		case <-tt.C:
			writeProgress(w, collectStates())
		case err := <-done:
			writeProgress(w, collectStates())
			return err
		}
	}
}

func cmdPull(rc *ollama.Registry, c *blob.DiskCache) error {
	model := flag.Arg(1)
	if model == "" {
		flag.Usage()
		os.Exit(1)
	}

	fmt.Fprintln(stdout, "Downloading...")
	return withProgress(stdout, func(ctx context.Context) error {
		return rc.Pull(ctx, c, model)
	})
}

func cmdPush(rc *ollama.Registry, c *blob.DiskCache) error {
	args := flag.Args()[1:]
	flag := flag.NewFlagSet("push", flag.ExitOnError)
	flagTo := flag.String("to", "", "Push to an alternate name.")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opp push <model>\n")
		flag.PrintDefaults()
	}
	flag.Parse(args)

	model := flag.Arg(0)
	if model == "" {
		fmt.Fprintf(os.Stderr, "error: missing model name\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Pushing %s to %s\n", model, *flagTo)
	return withProgress(stdout, func(ctx context.Context) error {
		return rc.Push(ctx, c, model, &ollama.PushParams{
			To: cmp.Or(*flagTo, model),
		})
	})
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
