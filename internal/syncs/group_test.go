package syncs

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestGroupErrors(t *testing.T) {
	var (
		errA = errors.New("a")
		errB = errors.New("b")
	)

	var g Group
	g.Go(func() error { return nil })
	g.Go(func() error { return errA })
	g.Go(func() error { return errB })

	err := g.Wait()
	if !errors.Is(err, errA) && !errors.Is(err, errB) {
		t.Errorf("expected %v to be errA or errB", err)
	}
}

func TestGroupUnlimited(t *testing.T) {
	synctest.Run(func() {
		var g Group
		// This will deadlock if there is a limit of 0, resulting in
		// panic by synctest
		g.Go(func() error { return nil })
		g.Go(func() error { return nil })
		if err := g.Wait(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// success
}

func TestGroupPanic(t *testing.T) {
	synctest.Run(func() {
		g, ctx := WithGroup(t.Context(), 3)

		go func() {
			g.Go(func() error { <-ctx.Done(); return nil })
			g.Go(func() error { panic("a") })
			g.Go(func() error { return errors.New("should not run") })
		}()

		defer func() {
			got := recover()
			if got != "a" {
				t.Errorf("got = %v; want %v", got, "a")
			}
		}()

		synctest.Wait()
		if err := g.Wait(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		t.Fatal("expected panic")
	})
}

func TestWithGroupCancel(t *testing.T) {
	t.Run("cancel before Go", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		g, _ := WithGroup(ctx, 0)

		cancel()
		g.Go(func() error { t.Fatal("should not run"); return nil })

		if err := g.Wait(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("cancel while Go is waiting", func(t *testing.T) {
		synctest.Run(func() {
			ctx, cancel := context.WithCancel(t.Context())

			// Limit 1
			g, _ := WithGroup(ctx, 1)
			go func() {
				// Start 1st goroutine
				g.Go(func() error { <-ctx.Done(); return nil })

				// Wait until the context cancels the first Go
				// routine, and prevents this Go starting
				// another goroutine.
				g.Go(func() error { t.Error("unexpected Go run"); return nil })
			}()

			// Wait for 1st goroutine to start
			synctest.Wait()

			// Cancel the context
			cancel()

			// Wait for 1st goroutine to finish.
			if err := g.Wait(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	})
}

func TestGroupLimit(t *testing.T) {
	synctest.Run(func() {
		ch := make(chan int)
		checkRecv := func(want int) {
			var got int
			select {
			case got = <-ch:
			default:
			}
			if got != want {
				t.Errorf("got %v; want %v", got, want)
			}
		}

		var g Group
		g.SetLimit(1)
		go func() {
			g.Go(func() error { ch <- 1; return nil })
			g.Go(func() error { ch <- 2; return nil })
		}()

		synctest.Wait()
		checkRecv(1)
		checkRecv(0)    // 2nd goroutine should not be allowed to run until 1st is done
		synctest.Wait() // wait for 1st goroutine to finish
		checkRecv(2)    // 2nd goroutine should run now
		if err := g.Wait(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
