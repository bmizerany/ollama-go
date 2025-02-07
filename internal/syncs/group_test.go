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

func TestGroupReset(t *testing.T) {
	var g Group
	if g.Limit() != 0 {
		t.Errorf("limit = %v; want 0", g.Limit())
	}
	g.Go(func() error { return errors.New("boom") })
	if err := g.Wait(); err == nil {
		t.Fatal("expected error")
	}

	g.Reset(2)
	if g.Limit() != 2 {
		t.Errorf("limit = %v; want 2", g.Limit())
	}
	if err := g.Wait(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
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
		g.Reset(1)
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
