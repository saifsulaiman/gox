package goweb

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var ErrRequestTimeout = errors.New("request exceeded configured timeout")

// Timeout returns a middleware that cancels the request context after the specified duration.
// Leverages context.WithTimeoutCause to record the exact timeout reason.
func Timeout(d time.Duration) HandlerFunc {
	return func(c *Context) error {
		ctx, cancel := context.WithTimeoutCause(c.Context(), d, ErrRequestTimeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)

		err := c.Next()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), ErrRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return c.AbortWithJSON(http.StatusGatewayTimeout, H{
				"error": "Request timeout: processing exceeded allocated deadline",
			})
		}
		return err
	}
}

// BodyLimit restricts the maximum allowable request body size in bytes.
// If the payload exceeds the limit, it rejects the request with 413 Payload Too Large.
func BodyLimit(maxBytes int64) HandlerFunc {
	return func(c *Context) error {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		return c.Next()
	}
}

type etagRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (r *etagRecorder) WriteHeader(code int) {
	r.statusCode = code
}

func (r *etagRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

// ETag automatically generates strong SHA-256 ETags for GET requests.
// If the client's If-None-Match header matches the computed ETag, it sends 304 Not Modified.
func ETag() HandlerFunc {
	return func(c *Context) error {
		if c.Method() != http.MethodGet {
			return c.Next()
		}

		rec := &etagRecorder{
			ResponseWriter: c.Writer,
			statusCode:     http.StatusOK,
		}
		origWriter := c.Writer
		c.Writer = rec
		defer func() {
			c.Writer = origWriter
		}()

		if err := c.Next(); err != nil {
			return err
		}

		bodyBytes := rec.body.Bytes()
		h := sha256.Sum256(bodyBytes)
		etag := `"` + hex.EncodeToString(h[:16]) + `"`

		c.SetHeader("ETag", etag)
		if c.Header("If-None-Match") == etag {
			origWriter.WriteHeader(http.StatusNotModified)
			return nil
		}

		origWriter.WriteHeader(rec.statusCode)
		_, err := origWriter.Write(bodyBytes)
		return err
	}
}

// CircuitState represents the current state of a circuit breaker.
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateHalfOpen
	StateOpen
)

// CircuitBreakerOptions configures circuit breaker thresholds.
type CircuitBreakerOptions struct {
	FailureThreshold int           // Number of consecutive failures to open circuit (default: 5)
	CooldownDuration time.Duration // Time to wait before testing recovery (default: 10s)
}

// CircuitBreaker provides fast-failure protection when downstream handlers continuously fail.
func CircuitBreaker(opts ...CircuitBreakerOptions) HandlerFunc {
	var opt CircuitBreakerOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	threshold := cmp.Or(opt.FailureThreshold, 5)
	cooldown := cmp.Or(opt.CooldownDuration, 10*time.Second)

	var mu sync.RWMutex
	var failures int
	var state CircuitState = StateClosed
	var lastStateChange atomic.Int64
	lastStateChange.Store(time.Now().UnixNano())

	return func(c *Context) error {
		mu.RLock()
		currentState := state
		now := time.Now().UnixNano()
		cooldownPassed := (now - lastStateChange.Load()) > cooldown.Nanoseconds()
		mu.RUnlock()

		if currentState == StateOpen {
			if cooldownPassed {
				mu.Lock()
				if state == StateOpen {
					state = StateHalfOpen
					lastStateChange.Store(time.Now().UnixNano())
				}
				mu.Unlock()
			} else {
				return c.JSON(http.StatusServiceUnavailable, H{
					"error": "Circuit breaker is OPEN: downstream service temporarily unavailable",
				})
			}
		}

		err := c.Next()

		mu.Lock()
		defer mu.Unlock()
		if err != nil || c.StatusCode() >= 500 {
			failures++
			if failures >= threshold {
				state = StateOpen
				lastStateChange.Store(time.Now().UnixNano())
			}
		} else {
			if state == StateHalfOpen || failures > 0 {
				state = StateClosed
				failures = 0
			}
		}

		return err
	}
}
