package syncs

import (
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

	errs := g.Wait()
	if !errors.Is(errs, errA) {
		t.Errorf("expected %v to be in %v", errA, errs)
	}
	if !errors.Is(errs, errB) {
		t.Errorf("expected %v to be in %v", errB, errs)
	}
}

func TestGroupPanic(t *testing.T) {
	var g Group

	// This should not cause a crash, but instead be repanicked in the Wait
	// call.
	g.Go(func() error { panic("a") })

	defer func() {
		got := recover()
		if got != "a" {
			t.Errorf("got = %v; want %v", got, "a")
		}
	}()

	if err := g.Wait(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	t.Fatal("expected panic")
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

		g := NewGroup(1)
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
