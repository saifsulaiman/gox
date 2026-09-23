package handler

import (
	"cmp"
	"net/http"
	"time"

	"github.com/goxlang/gox/pkg/goweb"
)

// IssueTokenHandler returns a handler that generates test JWT tokens with RBAC claims.
func IssueTokenHandler(jwtSecret string) goweb.HandlerFunc {
	return func(c *goweb.Context) error {
		var req struct {
			Role string `json:"role"`
		}
		_ = c.BindJSON(&req)
		role := cmp.Or(req.Role, "admin")

		token, err := goweb.GenerateJWT(goweb.JWTClaims{
			Subject:     "usr-" + role,
			Username:    role + "_user",
			Roles:       []string{role},
			Permissions: []string{"products:read", "products:write"},
			ExpiresAt:   time.Now().Add(24 * time.Hour).Unix(),
		}, jwtSecret)
		if err != nil {
			return c.Error(http.StatusInternalServerError, err.Error())
		}

		return c.JSON(http.StatusOK, goweb.H{
			"token": token,
			"role":  role,
			"type":  "Bearer",
		})
	}
}
