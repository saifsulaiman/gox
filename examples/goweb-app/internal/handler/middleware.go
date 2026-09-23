package handler

import (
	"github.com/goxlang/gox/pkg/goweb"
)

// RegisterMiddleware configures the production middleware chain on the Engine.
func RegisterMiddleware(app *goweb.Engine) {
	app.Use(
		goweb.Logger(),
		goweb.Recover(),
		goweb.Security(),
		goweb.CORS(goweb.DefaultCORSOptions),
		goweb.RateLimiter(5000, 10000), // 5,000 req/sec token bucket
		func(c *goweb.Context) error {
			if ip := c.IP(); ip != "" {
				_, _ = c.Redis().HyperLogLogAdd(c.Context(), "hll:visitors:daily", ip)
			}
			return c.Next()
		},
	)
}
