package syncs

import (
	"errors"
	"runtime"
	"sync"
)

// Group manages a Group of goroutines, limiting the number of concurrent
// goroutines to n, and collecting any errors they return.
type Group struct {
	sem chan struct{}
	wg  sync.WaitGroup

	mu   sync.Mutex
	errs []error
}

// NewGroup returns a new group limited to n goroutines. If n is 0, the limit
// is set to the number of logical CPUs.
//
// Users that want "unlimited" concurrency should use a very large number.
func NewGroup(n int) *Group {
	if n == 0 {
		n = runtime.GOMAXPROCS(0)
	}
	return &Group{sem: make(chan struct{}, n)}
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
	g.sem <- struct{}{}
	go func() {
		defer func() {
			<-g.sem
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
// It must not be called concurrently with [Group.Go] or another call to
// [Group.Wait].
//
// Calls to [Group.Go] after a call to [Group.Wait] will result in undefined
// behavior.
func (g *Group) Wait() error {
	// no need to guard g.errs here, since we know all goroutines have
	// finished.
	g.wg.Wait()
	return errors.Join(g.errs...)
}
