package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/bmizerany/ollama-go/blob"
	"github.com/bmizerany/ollama-go/client/ollama"

	"rsc.io/omap"
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

	ansiHideCursor()
	defer ansiShowCursor()

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

	tt := time.NewTicker(time.Second)
	for {
		select {
		case <-tt.C:
			pmu.Lock()
			drawProgress(progress, true)
			pmu.Unlock()
		case err := <-done:
			drawProgress(progress, false)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println("done")
			return
		}
	}
}

type state struct {
	n, size int64
	err     error
}

// drawProgress draws progress bars for each blob being downloaded. The
// terminal codes are used to update redraw from the start position. If the
// start position is 0, the current position is used and returned for the next
// call.
func drawProgress(p *omap.MapFunc[blob.Digest, state], restorePos bool) {
	// bookmark the current position as the start position
	ansiSavePos()
	if restorePos {
		defer ansiRestorePos()
	}

	for d, s := range p.All() {
		if s.err != nil {
			fmt.Printf("%s % -3d%% %v/%v ! %v\n",
				d.Short(),
				s.n*100/s.size,
				FormatBytes(s.n),
				FormatBytes(s.size),
				s.err)
		} else {
			fmt.Printf("%s % 3d%% %v/%v\n",
				d.Short(),
				s.n*100/s.size,
				FormatBytes(s.n),
				FormatBytes(s.size))
		}
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

func ansiSavePos()    { fmt.Print("\033[s") }
func ansiRestorePos() { fmt.Print("\033[u") }
func ansiHideCursor() { fmt.Print("\033[?25l") }
func ansiShowCursor() { fmt.Print("\033[?25h") }
