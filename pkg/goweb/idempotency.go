package goweb

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// IdempotencyOptions configures the Idempotency middleware.
type IdempotencyOptions struct {
	HeaderKey     string        // Default: "Idempotency-Key"
	LockTTL       time.Duration // Default: 30 seconds
	RetentionTTL  time.Duration // Default: 24 hours
	KeyPrefix     string        // Default: "idempotency:"
	Required      bool          // If true, requests missing the header return 400 Bad Request
}

type cachedIdempotentResponse struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
}

// memoryIdempotencyStore provides in-memory deduplication when Redis is not active.
type memoryIdempotencyStore struct {
	mu        sync.RWMutex
	inFlight  map[string]time.Time
	completed map[string]cachedIdempotentResponse
}

var defaultMemStore = &memoryIdempotencyStore{
	inFlight:  make(map[string]time.Time),
	completed: make(map[string]cachedIdempotentResponse),
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// Idempotency returns a middleware that guarantees at-most-once execution for mutating HTTP methods (POST, PUT, PATCH).
// If a request with the same Idempotency-Key is currently in progress, it returns 409 Conflict.
// If a previous request with the same Idempotency-Key completed, its cached response is safely replayed.
func Idempotency(opts ...IdempotencyOptions) HandlerFunc {
	var opt IdempotencyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	headerKey := cmp.Or(opt.HeaderKey, "Idempotency-Key")
	lockTTL := cmp.Or(opt.LockTTL, 30*time.Second)
	retentionTTL := cmp.Or(opt.RetentionTTL, 24*time.Hour)
	keyPrefix := cmp.Or(opt.KeyPrefix, "idempotency:")

	return func(c *Context) error {
		// Only enforce on mutating requests
		method := c.Method()
		if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete {
			return c.Next()
		}

		key := c.Header(headerKey)
		if key == "" {
			key = c.Header("X-" + headerKey)
		}

		if key == "" {
			if opt.Required {
				return c.AbortWithJSON(http.StatusBadRequest, H{
					"error": fmt.Sprintf("Missing required header: %s", headerKey),
				})
			}
			return c.Next()
		}

		// Sanitize key with SHA256 hash to prevent Redis key injection
		h := sha256.Sum256([]byte(key))
		storageKey := keyPrefix + hex.EncodeToString(h[:])
		lockKey := storageKey + ":lock"

		redis := c.Redis()
		ctx := c.Context()

		if redis != nil && !redis.isMemory {
			// Check if already completed
			cachedJSON, err := redis.Get(ctx, storageKey)
			if err == nil && cachedJSON != "" {
				var cached cachedIdempotentResponse
				if jsonErr := json.Unmarshal([]byte(cachedJSON), &cached); jsonErr == nil {
					for k, v := range cached.Headers {
						c.SetHeader(k, v)
					}
					c.SetHeader("X-Cache-Lookup", "HIT-IDEMPOTENT")
					return c.AbortWithBlob(cached.StatusCode, c.Header("Content-Type"), cached.Body)
				}
			}

			// Acquire distributed lock for in-flight request
			lock, err := redis.Lock(ctx, lockKey, lockTTL)
			if err != nil {
				return c.AbortWithJSON(http.StatusConflict, H{
					"error": "A request with this Idempotency-Key is currently being processed",
				})
			}
			defer func() {
				_ = lock.Unlock(ctx)
			}()

			// Capture response
			recorder := &responseRecorder{
				ResponseWriter: c.Writer,
				statusCode:     http.StatusOK,
			}
			origWriter := c.Writer
			c.Writer = recorder
			defer func() {
				c.Writer = origWriter
			}()

			err = c.Next()

			// Cache response on successful or safe completion (2xx, 3xx, 4xx)
			if recorder.statusCode < 500 {
				cached := cachedIdempotentResponse{
					StatusCode: recorder.statusCode,
					Headers: map[string]string{
						"Content-Type": c.Writer.Header().Get("Content-Type"),
					},
					Body: recorder.body.Bytes(),
				}
				data, _ := json.Marshal(cached)
				_ = redis.Set(ctx, storageKey, string(data), retentionTTL)
			}

			return err
		}

		// In-Memory Fallback
		defaultMemStore.mu.Lock()
		if cached, ok := defaultMemStore.completed[storageKey]; ok {
			defaultMemStore.mu.Unlock()
			c.SetHeader("X-Cache-Lookup", "HIT-IDEMPOTENT")
			return c.AbortWithBlob(cached.StatusCode, "application/json", cached.Body)
		}

		if expireTime, active := defaultMemStore.inFlight[storageKey]; active && time.Now().Before(expireTime) {
			defaultMemStore.mu.Unlock()
			return c.AbortWithJSON(http.StatusConflict, H{
				"error": "A request with this Idempotency-Key is currently being processed",
			})
		}

		defaultMemStore.inFlight[storageKey] = time.Now().Add(lockTTL)
		defaultMemStore.mu.Unlock()

		recorder := &responseRecorder{
			ResponseWriter: c.Writer,
			statusCode:     http.StatusOK,
		}
		origWriter := c.Writer
		c.Writer = recorder
		defer func() {
			c.Writer = origWriter
			defaultMemStore.mu.Lock()
			delete(defaultMemStore.inFlight, storageKey)
			defaultMemStore.mu.Unlock()
		}()

		err := c.Next()

		if recorder.statusCode < 500 {
			defaultMemStore.mu.Lock()
			defaultMemStore.completed[storageKey] = cachedIdempotentResponse{
				StatusCode: recorder.statusCode,
				Body:       recorder.body.Bytes(),
			}
			defaultMemStore.mu.Unlock()
		}

		return err
	}
}
