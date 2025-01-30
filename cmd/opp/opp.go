package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"runtime/trace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/client/ollama"
	"github.com/bmizerany/ollama-go/cmd/opp/internal/safetensors"
	"github.com/bmizerany/ollama-go/internal/syncs"
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

    OLLAMA_REGISTRY
	The base URL of the Ollama registry server (default https://ollama.com).
`

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

	rc, err := ollama.RegistryFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	err = func() error {
		switch cmd := flag.Arg(0); cmd {
		case "pull":
			return cmdPull(ctx, rc, c)
		case "push":
			return cmdPush(ctx, rc, c)
		case "import":
			return cmdImport(ctx, c)
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

func cmdPull(ctx context.Context, rc *ollama.Registry, c *blob.DiskCache) error {
	model := flag.Arg(1)
	if model == "" {
		flag.Usage()
		os.Exit(1)
	}

	m, err := rc.Resolve(ctx, model)
	if err != nil {
		return err
	}

	ctx = ollama.WithTrace(ctx, &ollama.Trace{
		PullUpdate: func(d blob.Digest, n, size int64, err error) {
			switch {
			case err != nil:
				fmt.Fprintf(stdout, "opp: downloading %s %d/%d ! %v\n", d.Short(), n, size, err)
			case n == 0:
				l := m.LookupLayer(d)
				mt, p, _ := mime.ParseMediaType(l.MediaType)
				mt, _ = strings.CutPrefix(mt, "application/vnd.ollama.image.")
				switch mt {
				case "tensor":
					fmt.Fprintf(stdout, "opp: downloading tensor %s %s\n", d.Short(), p["name"])
				default:
					fmt.Fprintf(stdout, "opp: downloading %s %s\n", d.Short(), l.MediaType)
				}
			}
		},
	})

	// TODO(bmizerany): there is a slight race here. we need for Resolve to
	// return a Manifest.Digest, and Pull should use that, always, or maybe
	// it should return the Ref (e.g "library/llama3.2:latest@sha....")
	// which would be used by Pull... this would allow pull to continue
	// taking just a name, and for those that want to call Resolve first
	// and avoid the race where the manifest changes between Resolve and
	// Pull, they can do that, safely.

	return rc.Pull(ctx, c, model)
}

func cmdPush(ctx context.Context, rc *ollama.Registry, c *blob.DiskCache) error {
	args := flag.Args()[1:]
	flag := flag.NewFlagSet("push", flag.ExitOnError)
	flagFrom := flag.String("from", "", "Use the manifest from a model by another name.")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opp push <model>\n")
		flag.PrintDefaults()
	}
	flag.Parse(args)

	model := flag.Arg(0)
	if model == "" {
		return fmt.Errorf("missing model argument")
	}

	from := cmp.Or(*flagFrom, model)
	m, err := ollama.Resolve(c, from)
	if err != nil {
		return err
	}

	ctx = ollama.WithTrace(ctx, &ollama.Trace{
		PushUpdate: func(d blob.Digest, n, size int64, err error) {
			switch {
			case err != nil:
				fmt.Fprintf(stdout, "opp: uploading %s %d/%d ! %v\n", d.Short(), n, size, err)
			case n == 0:
				l := m.LookupLayer(d)
				mt, p, _ := mime.ParseMediaType(l.MediaType)
				mt, _ = strings.CutPrefix(mt, "application/vnd.ollama.image.")
				switch mt {
				case "tensor":
					fmt.Fprintf(stdout, "opp: uploading tensor %s %s\n", d.Short(), p["name"])
				default:
					fmt.Fprintf(stdout, "opp: uploading %s %s\n", d.Short(), l.MediaType)
				}
			}
		},
	})

	return rc.Push(ctx, c, model, &ollama.PushParams{
		From: from,
	})
}

type trackingReader struct {
	io.Reader
	n *atomic.Int64
}

func (r *trackingReader) Read(p []byte) (n int, err error) {
	n, err = r.Reader.Read(p)
	r.n.Add(int64(n))
	return n, err
}

func cmdImport(ctx context.Context, c *blob.DiskCache) error {
	args := flag.Args()[1:]
	flag := flag.NewFlagSet("import", flag.ExitOnError)
	flagAs := flag.String("as", "", "Import using the provided name.")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opp import <SafetensorDir>\n")
		flag.PrintDefaults()
	}
	flag.Parse(args)

	dir := cmp.Or(flag.Arg(0), ".")
	fmt.Fprintf(os.Stderr, "Reading %s\n", dir)

	m, err := safetensors.Read(os.DirFS(dir))
	if err != nil {
		return err
	}

	var total int64
	var tt []*safetensors.Tensor
	for t, err := range m.Tensors() {
		if err != nil {
			return err
		}
		tt = append(tt, t)
		total += t.Size()
	}

	var n atomic.Int64
	done := make(chan error)
	go func() {
		layers := make([]*ollama.Layer, len(tt))
		g := syncs.NewGroup(0)
		var ctxErr error
		for i, t := range tt {
			if ctx.Err() != nil {
				// The context may cancel AFTER we exit the
				// loop, and so if we use ctx.Err() after the
				// loop we may report it as the error that
				// broke the loop, when it was not. This can
				// manifest as a false-negative, leading the
				// user to think their import failed when it
				// did not, so capture it if and only if we
				// exit the loop because of a ctx.Err() and
				// report it.
				ctxErr = ctx.Err()
				break
			}
			g.Go(func() (err error) {
				rc, err := t.Reader()
				if err != nil {
					return err
				}
				defer rc.Close()
				tr := &trackingReader{rc, &n}
				d, err := c.Import(tr, t.Size())
				if err != nil {
					return err
				}
				if err := rc.Close(); err != nil {
					return err
				}

				layers[i] = &ollama.Layer{
					Digest: d,
					Size:   t.Size(),
					MediaType: mime.FormatMediaType("application/vnd.ollama.image.tensor", map[string]string{
						"name":  t.Name(),
						"dtype": t.DataType(),
						"shape": t.Shape().String(),
					}),
				}

				return nil
			})
		}

		done <- func() error {
			if err := errors.Join(g.Wait(), ctxErr); err != nil {
				return err
			}
			m := &ollama.Manifest{Layers: layers}
			data, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				return err
			}
			d := blob.DigestFromBytes(data)
			err = blob.PutBytes(c, d, data)
			if err != nil {
				return err
			}
			return c.Link(*flagAs, d)
		}()
	}()

	fmt.Fprintf(stdout, "Importing %d tensors from %s\n", len(tt), dir)

	csiHideCursor(stdout)
	defer csiShowCursor(stdout)

	csiSavePos(stdout)
	writeProgress := func() {
		csiRestorePos(stdout)
		nn := n.Load()
		fmt.Fprintf(stdout, "Imported %s/%s bytes (%d%%)%s\n",
			formatNatural(nn),
			formatNatural(total),
			nn*100/total,
			ansiClearToEndOfLine,
		)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			writeProgress()
		case err := <-done:
			writeProgress()
			return err
		}
	}
}

func formatNatural(n int64) string {
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

const ansiClearToEndOfLine = "\033[K"

func csiSavePos(w io.Writer)    { fmt.Fprint(w, "\033[s") }
func csiRestorePos(w io.Writer) { fmt.Fprint(w, "\033[u") }
func csiHideCursor(w io.Writer) { fmt.Fprint(w, "\033[?25l") }
func csiShowCursor(w io.Writer) { fmt.Fprint(w, "\033[?25h") }

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
