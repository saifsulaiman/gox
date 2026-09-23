package goweb

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

type detachedContext struct {
	parent context.Context
}

func (d *detachedContext) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

func (d *detachedContext) Done() <-chan struct{} {
	return nil
}

func (d *detachedContext) Err() error {
	return nil
}

func (d *detachedContext) Value(key any) any {
	return d.parent.Value(key)
}

// DetachContext returns a context that inherits parent context values but is never canceled.
func DetachContext(parent context.Context) context.Context {
	if parent == nil {
		return context.Background()
	}
	return &detachedContext{parent: parent}
}

// Async runs a function in a background goroutine with a safely detached context.
// It preserves request context values (such as RequestID and trace attributes) while
// isolating the task from HTTP connection cancellation and catching panics.
func (c *Context) Async(fn func(ctx context.Context)) {
	detached := DetachContext(c.Context())
	logger := c.Logger()
	reqID := c.RequestID()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("Async worker recovered panic",
					slog.Any("panic", r),
					slog.String("request_id", reqID),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
		fn(detached)
	}()
}
