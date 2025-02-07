package syncs

import (
	"cmp"
	"context"
	"sync"
)

// Group is like errgroup.Group, but it recovers from panics in the goroutines
// it runs and repanics them in the goroutine belonging to the caller of
// [Group.Wait]. If all groutines should be shutdown after a panic, use
// [WithGroup].
type Group struct {
	ctx    context.Context
	cancel context.CancelFunc

	sem gate
	wg  sync.WaitGroup

	mu  sync.Mutex
	err error
	pe  any // panic error
}

// WithGroup returns a [Group] with the limit, and a context that is canceled
// if any goroutine started with [Group.Go] panics.
func WithGroup(ctx context.Context, limit int) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	g := &Group{ctx: ctx, cancel: cancel}
	g.SetLimit(limit)
	return g, ctx
}

func (g *Group) ctxErr() error {
	if g.ctx == nil {
		return nil
	}
	return g.ctx.Err()
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
	if g.ctxErr() != nil {
		// fast path for already canceled context
		return
	}

	g.sem.take()
	g.wg.Add(1)

	// context may have been canceled while we were waiting for a token;
	// check again.
	if g.ctxErr() != nil {
		return
	}
	go func() {
		defer func() {
			g.sem.release()
			g.wg.Done()
			if r := recover(); r != nil {
				g.mu.Lock()
				g.pe = cmp.Or(g.pe, r)
				g.mu.Unlock()
				if g.cancel != nil {
					g.cancel()
				}
			}
		}()
		if err := f(); err != nil {
			g.mu.Lock()
			g.err = cmp.Or(g.err, err)
			g.mu.Unlock()
		}
	}()
}

// Wait waits for all running goroutines in the Group to finish. The first
// error encountered is returned. Panics in the goroutines are recorded and and
// repanicked in the Wait caller's goroutine.
//
// It is not safe for concurrent use.
func (g *Group) Wait() error {
	// no need to guard g.errs here, since we know all goroutines have
	// finished.
	g.wg.Wait()
	if g.pe != nil {
		panic(g.pe)
	}
	return g.err
}

// SetLimit sets the limit of the Group. If n is 0, the Group is unlimited.
func (g *Group) SetLimit(n int) {
	g.sem = nil
	if n > 0 {
		g.sem = make(gate, n)
	}
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
