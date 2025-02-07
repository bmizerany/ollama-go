package syncs

import (
	"cmp"
	"errors"
	"runtime"
	"sync"
)

// Group manages a group of goroutines started with [Group.Go] which can be
// waited on with [Group.Wait]. All errors returned by the goroutines are
// collected and returned by [Group.Wait] once all goroutines have finished. If
// a goroutine panics, the panic is recorded and repanicked in the [Group.Wait]
// callers goroutine.
type Group struct {
	sem chan struct{}
	wg  sync.WaitGroup

	mu   sync.Mutex
	errs []error
	pe   any // panic error
}

// NewGroup creates a new Group with the given limit. If limit is 0, the limit is
// set to [runtime.GOMAXPROCS(0)].
func NewGroup(limit int) *Group {
	limit = cmp.Or(limit, runtime.GOMAXPROCS(0))
	return &Group{sem: make(chan struct{}, limit)}
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
	if g.sem != nil {
		g.sem <- struct{}{}
	}
	go func() {
		defer func() {
			if g.sem != nil {
				<-g.sem
			}
			if r := recover(); r != nil {
				g.mu.Lock()
				g.pe = r
				g.mu.Unlock()
			}
			g.wg.Done()
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
