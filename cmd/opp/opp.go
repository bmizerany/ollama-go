package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/client/ollama"

	"rsc.io/omap"
)

var stdout io.Writer = os.Stdout

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

	csiHideCursor(stdout)
	defer csiShowCursor(stdout)

	ctx := context.Background()

	var pmu sync.Mutex
	progress := omap.NewMapFunc[blob.Digest, state](blob.Digest.Compare)

	done := make(chan error)
	go func() {
		ctx := ollama.WithTrace(ctx, &ollama.Trace{
			DownloadUpdate: func(d blob.Digest, n int64, size int64, err error) {
				pmu.Lock()
				progress.Set(d, state{n: n, size: size, err: err})
				pmu.Unlock()
			},
		})

		done <- rc.Pull(ctx, c, model)
	}()

	csiSavePos(stdout)
	writeProgress(stdout, progress)
	tt := time.NewTicker(time.Second)
	for {
		select {
		case <-tt.C:
			pmu.Lock()
			csiRestorePos(stdout)
			writeProgress(stdout, progress)
			pmu.Unlock()
		case err := <-done:
			writeProgress(stdout, progress)
			if err != nil {
				log.Fatal(err)
			}
			return
		}
	}
}

type state struct {
	n, size int64
	err     error
}

// writeProgress draws progress bars for each blob being downloaded. The
// terminal codes are used to update redraw from the start position. If the
// start position is 0, the current position is used and returned for the next
// call.
func writeProgress(w io.Writer, p *omap.MapFunc[blob.Digest, state]) {
	for d, s := range p.All() {
		if s.err != nil {
			fmt.Fprintf(w, "%s %3d%% %v/%v ! %v\n",
				d.Short(),
				s.n*100/s.size,
				FormatBytes(s.n),
				FormatBytes(s.size),
				s.err)
		} else {
			fmt.Fprintf(w, "%s %3d%% %v/%v\n",
				d.Short(),
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
