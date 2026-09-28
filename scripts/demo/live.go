package main

import (
	"context"
	"net/http"
	"sync"
)

// demoApplication rotates only the synthetic web handler so browser tests can
// exercise fresh credentials and interrupted event streams against real HTTP.
type demoApplication struct {
	mu      sync.RWMutex
	handler http.Handler
	ctx     context.Context
	cancel  context.CancelFunc
}

func newDemoApplication(h http.Handler) *demoApplication {
	app := &demoApplication{}
	app.reset(h)
	return app
}
func (a *demoApplication) reset(h http.Handler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.handler = h
}
func (a *demoApplication) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	h, appCtx := a.handler, a.ctx
	a.mu.RUnlock()
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(appCtx, cancel)
	defer stop()
	defer cancel()
	h.ServeHTTP(w, r.WithContext(ctx))
}
