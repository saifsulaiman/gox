package goweb

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goxlang/gox/pkg/goxrt"
)

// Logger returns a modern structured access logging middleware using log/slog.
func Logger() HandlerFunc {
	return func(c *Context) error {
		start := time.Now()
		path := c.Request.URL.Path
		if rawQuery := c.Request.URL.RawQuery; rawQuery != "" {
			path += "?" + rawQuery
		}

		err := c.Next()

		latency := time.Since(start)
		status := c.StatusCode()
		clientIP := c.RealIP()
		method := c.Request.Method
		logger := c.Logger()

		attrs := []slog.Attr{
			slog.Int("status", status),
			slog.Duration("latency", latency),
			slog.String("ip", clientIP),
			slog.String("method", method),
			slog.String("path", path),
		}

		if reqID := c.Header("X-Request-ID"); reqID != "" {
			attrs = append(attrs, slog.String("req_id", reqID))
		}

		ctx := c.Context()
		if status >= 500 {
			logger.LogAttrs(ctx, slog.LevelError, "HTTP request failed", attrs...)
		} else if status >= 400 {
			logger.LogAttrs(ctx, slog.LevelWarn, "HTTP request warning", attrs...)
		} else {
			logger.LogAttrs(ctx, slog.LevelInfo, "HTTP request handled", attrs...)
		}

		return err
	}
}

// Recover returns a panic recovery middleware that catches panics,
// logs structured diagnostics with slog, and returns a JSON 500.
func Recover() HandlerFunc {
	return func(c *Context) error {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				c.Logger().ErrorContext(c.Context(), "Goweb recovered panic",
					slog.Any("panic", r),
					slog.String("stack", string(stack)),
				)
				if !c.written {
					_ = c.AbortWithError(http.StatusInternalServerError, fmt.Errorf("internal server error"))
				}
			}
		}()
		return c.Next()
	}
}

// CORSOptions configures the Cross-Origin Resource Sharing middleware.
type CORSOptions struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           int
}

// DefaultCORSOptions provides open development CORS settings.
var DefaultCORSOptions = CORSOptions{
	AllowOrigins:     []string{"*"},
	AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"},
	AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Request-ID"},
	AllowCredentials: true,
	MaxAge:           86400,
}

// CORS returns a CORS middleware with custom options.
func CORS(opts CORSOptions) HandlerFunc {
	origins := "*"
	if len(opts.AllowOrigins) > 0 && !slices.Contains(opts.AllowOrigins, "*") {
		origins = strings.Join(opts.AllowOrigins, ", ")
	}
	methods := "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS"
	if len(opts.AllowMethods) > 0 {
		methods = strings.Join(opts.AllowMethods, ", ")
	}

	return func(c *Context) error {
		c.SetHeader("Access-Control-Allow-Origin", origins)
		c.SetHeader("Access-Control-Allow-Methods", methods)
		c.SetHeader("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID, Accept")
		if opts.AllowCredentials {
			c.SetHeader("Access-Control-Allow-Credentials", "true")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return nil
		}
		return c.Next()
	}
}

// Security adds standard production security HTTP headers.
func Security() HandlerFunc {
	return func(c *Context) error {
		c.SetHeader("X-Content-Type-Options", "nosniff")
		c.SetHeader("X-Frame-Options", "DENY")
		c.SetHeader("X-XSS-Protection", "1; mode=block")
		c.SetHeader("Referrer-Policy", "strict-origin-when-cross-origin")
		return c.Next()
	}
}

// HeaderXRequestID is the standard HTTP header for tracing request IDs.
const HeaderXRequestID = "X-Request-ID"

// RequestID generates a unique request ID and attaches it to request and response headers.
func RequestID() HandlerFunc {
	return func(c *Context) error {
		reqID := c.Header(HeaderXRequestID)
		if reqID == "" {
			buf := make([]byte, 16)
			_, _ = rand.Read(buf)
			reqID = hex.EncodeToString(buf)
		}
		c.SetHeader("X-Request-ID", reqID)
		c.Set("RequestID", reqID)
		return c.Next()
	}
}

// tokenBucket manages rate limiting state per client IP.
type tokenBucket struct {
	tokens     float64
	capacity   float64
	fillRate   float64
	lastRefill time.Time
	mu         sync.Mutex
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.fillRate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}

// RateLimiter returns an IP-based token-bucket rate limiting middleware.
func RateLimiter(ratePerSec float64, burst int) HandlerFunc {
	var mu sync.RWMutex
	clients := make(map[string]*tokenBucket)

	return func(c *Context) error {
		ip := c.RealIP()
		mu.RLock()
		bucket, ok := clients[ip]
		mu.RUnlock()

		if !ok {
			mu.Lock()
			bucket, ok = clients[ip]
			if !ok {
				bucket = &tokenBucket{
					tokens:     float64(burst),
					capacity:   float64(burst),
					fillRate:   ratePerSec,
					lastRefill: time.Now(),
				}
				clients[ip] = bucket
			}
			mu.Unlock()
		}

		if !bucket.allow() {
			c.SetHeader("Retry-After", "1")
			return c.AbortWithError(http.StatusTooManyRequests, fmt.Errorf("rate limit exceeded"))
		}
		return c.Next()
	}
}

// RequestArenaMiddleware allocates and manages an active Request Arena per request.
func RequestArenaMiddleware() HandlerFunc {
	return func(c *Context) error {
		if c.arena == nil {
			arena := goxrt.GetArena()
			defer goxrt.PutArena(arena)
			c.arena = arena
		}
		return c.Next()
	}
}
