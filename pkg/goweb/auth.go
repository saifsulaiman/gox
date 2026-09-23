package goweb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// User represents an authenticated identity in GoxWeb.
type User struct {
	ID          string         `json:"id"`
	Username    string         `json:"username"`
	Roles       []string       `json:"roles"`
	Permissions []string       `json:"permissions"`
	Claims      map[string]any `json:"claims"`
}

// HasRole checks whether the user possesses the given role.
func (u *User) HasRole(role string) bool {
	if u == nil {
		return false
	}
	return slices.Contains(u.Roles, role)
}

// HasPermission checks whether the user possesses the given permission.
func (u *User) HasPermission(perm string) bool {
	if u == nil {
		return false
	}
	return slices.Contains(u.Permissions, perm)
}

const userContextKey = "goweb:user"

// User retrieves the authenticated user from the request context, or nil if unauthenticated.
func (c *Context) User() *User {
	val, ok := c.Get(userContextKey)
	if !ok || val == nil {
		return nil
	}
	if u, ok := val.(*User); ok {
		return u
	}
	return nil
}

// SetUser attaches the authenticated user to the request context.
func (c *Context) SetUser(u *User) {
	c.Set(userContextKey, u)
}

// BearerAuth validates a Bearer token from the Authorization header using a custom validator function.
func BearerAuth(validator func(token string) (*User, error)) HandlerFunc {
	return func(c *Context) error {
		authHeader := c.Header("Authorization")
		if authHeader == "" || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			return c.AbortWithJSON(http.StatusUnauthorized, H{"error": "Missing or invalid Authorization header"})
		}

		token := strings.TrimSpace(authHeader[7:])
		user, err := validator(token)
		if err != nil || user == nil {
			return c.AbortWithJSON(http.StatusUnauthorized, H{"error": "Invalid or expired authorization token"})
		}

		c.SetUser(user)
		return c.Next()
	}
}

// JWTClaims holds standard and custom claims for HMAC-SHA256 tokens.
type JWTClaims struct {
	Subject     string   `json:"sub"`
	Username    string   `json:"username"`
	ExpiresAt   int64    `json:"exp"`
	IssuedAt    int64    `json:"iat"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// GenerateJWT creates a signed HMAC-SHA256 JWT token.
func GenerateJWT(claims JWTClaims, secret string) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature, nil
}

// VerifyJWT parses and validates an HMAC-SHA256 JWT string.
func VerifyJWT(tokenString, secret string) (*JWTClaims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT token")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	expectedSig := mac.Sum(nil)

	actualSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(expectedSig, actualSig) {
		return nil, errors.New("invalid signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload encoding")
	}

	var claims JWTClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, errors.New("invalid claims format")
	}

	if claims.ExpiresAt > 0 && time.Now().Unix() > claims.ExpiresAt {
		return nil, errors.New("token has expired")
	}

	return &claims, nil
}

// JWTAuth provides zero-dependency HMAC-SHA256 JWT token authentication middleware.
func JWTAuth(secret string) HandlerFunc {
	return BearerAuth(func(token string) (*User, error) {
		claims, err := VerifyJWT(token, secret)
		if err != nil {
			return nil, err
		}
		return &User{
			ID:          claims.Subject,
			Username:    claims.Username,
			Roles:       claims.Roles,
			Permissions: claims.Permissions,
		}, nil
	})
}

// RequireRoles restricts endpoint access to authenticated users having AT LEAST ONE of the specified roles.
func RequireRoles(roles ...string) HandlerFunc {
	return func(c *Context) error {
		u := c.User()
		if u == nil {
			return c.AbortWithJSON(http.StatusUnauthorized, H{"error": "Authentication required"})
		}
		hasAny := false
		for _, r := range roles {
			if u.HasRole(r) {
				hasAny = true
				break
			}
		}
		if !hasAny {
			return c.AbortWithJSON(http.StatusForbidden, H{
				"error": fmt.Sprintf("Forbidden: requires one of roles %v", roles),
			})
		}
		return c.Next()
	}
}

// RequirePermissions restricts endpoint access to authenticated users possessing ALL specified permissions.
func RequirePermissions(perms ...string) HandlerFunc {
	return func(c *Context) error {
		u := c.User()
		if u == nil {
			return c.AbortWithJSON(http.StatusUnauthorized, H{"error": "Authentication required"})
		}
		for _, p := range perms {
			if !u.HasPermission(p) {
				return c.AbortWithJSON(http.StatusForbidden, H{
					"error": fmt.Sprintf("Forbidden: missing required permission '%s'", p),
				})
			}
		}
		return c.Next()
	}
}
