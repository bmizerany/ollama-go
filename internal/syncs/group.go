package syncs

import (
	"errors"
	"sync"
)

// Group manages a group of goroutines started with [Group.Go] which can be
// waited on with [Group.Wait]. All errors returned by the goroutines are
// collected and returned by [Group.Wait] once all goroutines have finished. If
// a goroutine panics, the panic is recorded and repanicked in the [Group.Wait]
// callers goroutine.
type Group struct {
	sem gate
	wg  sync.WaitGroup

	mu   sync.Mutex
	errs []error
	pe   any // panic error
}

// Go runs the given function f in a goroutine, blocking if the group is at its
// limit. Any error returned by f is recorded and returned in the next call to
// [Group.Wait].
//
// All calls to Go should be made before calling [Group.Wait]. If a call to Go
// happens after [Group.Wait], the behavior is undefined.
//
// It is not safe for concurrent use.
func (g *Group) Go(f func() error) {
	g.wg.Add(1)
	g.sem.take()
	go func() {
		defer func() {
			defer g.wg.Done()
			if r := recover(); r != nil {
				g.mu.Lock()
				g.pe = r
				g.mu.Unlock()
				return
			}
			// release our token here to avoid letting goroutines
			// start if there was a panic.
			g.sem.release()
		}()
		if err := f(); err != nil {
			g.mu.Lock()
			g.errs = append(g.errs, err)
			g.mu.Unlock()
		}
	}()
}

// Wait waits for all running goroutines in the Group to finish. All errors are
// returned using [errors.Join].
//
// It must not be called concurrently with [Group.Go], but is safe to call
// concurrently with itself.
func (g *Group) Wait() error {
	// no need to guard g.errs here, since we know all goroutines have
	// finished.
	g.wg.Wait()
	if g.pe != nil {
		panic(g.pe)
	}
	return errors.Join(g.errs...)
}

// Reset resets the Group to its initial state. It should only be called before
// any calls to [Group.Go] or after a call to [Group.Wait].
//
// It is not safe for concurrent use.
func (g *Group) Reset(limit int) {
	g.errs = nil
	g.pe = nil
	g.sem = nil
	if limit > 0 {
		g.sem = make(chan struct{}, limit)
	}
}

// Limit returns the limit of the Group.
func (g *Group) Limit() int {
	return cap(g.sem)
}

type gate chan struct{}

func (g gate) take() {
	if cap(g) > 0 {
		g <- struct{}{}
	}
}

func (g gate) release() {
	if cap(g) > 0 {
		<-g
	}
}
