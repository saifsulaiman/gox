package goweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goxlang/gox/pkg/goxrt"
)

func TestContextUltimateCoverage(t *testing.T) {
	// 1. Params.All early break
	ps := Params{
		{Key: "k1", Value: "v1"},
		{Key: "k2", Value: "v2"},
	}
	for range ps.All() {
		break // triggers the !yield return branch
	}

	// 2. Context Arena methods
	cNil := &Context{}
	if cNil.Arena() != nil {
		t.Error("expected nil arena for nil context")
	}

	rawArena := goxrt.NewArena()
	cArena := &Context{arena: rawArena}
	if cArena.Arena() != rawArena {
		t.Error("expected explicit arena")
	}

	reqArenaCtx := context.WithValue(context.Background(), goxrt.RequestArenaContextKey(), rawArena)
	cReqArena := &Context{
		Request: (&http.Request{}).WithContext(reqArenaCtx),
	}
	if cReqArena.Arena() == nil {
		t.Error("expected arena from request context")
	}

	// 3. RequestID branches
	cID := &Context{}
	if cID.RequestID() != "" {
		t.Error("expected empty string for nil request and empty store")
	}
	cID.Set("RequestID", 99999) // non-string value
	if cID.RequestID() != "" {
		t.Error("expected empty string for non-string RequestID")
	}
	cID.Set("RequestID", "") // empty string value
	if cID.RequestID() != "" {
		t.Error("expected empty string for empty string RequestID")
	}

	// 4. IP, Context(), Param methods
	req, _ := http.NewRequest("GET", "/test?a=1", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	cFull := &Context{
		Request: req,
		Params:  Params{{Key: "id", Value: "42"}},
	}
	if cFull.Context() != req.Context() {
		t.Error("Context() should return request context")
	}
	if cFull.IP() != "192.0.2.1" {
		t.Errorf("expected 192.0.2.1, got %s", cFull.IP())
	}
	if cFull.Param("id") != "42" {
		t.Errorf("expected 42, got %s", cFull.Param("id"))
	}
}

func TestAppUltimateCoverage(t *testing.T) {
	app := New(Config{})
	db, err := NewDatabase(DBConfig{Driver: DBSQLite, DSN: "file::memory:?cache=shared&mode=rwc"})
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()

	// 1. app.DB with postgres
	app.postgres = db
	if app.DB() != db {
		t.Error("expected postgres db")
	}

	// 2. app.Run with invalid address
	errRun := app.Run("invalid-host-addr:9999999")
	if errRun == nil {
		t.Error("expected error running with invalid address")
	}
}

func TestAsyncUltimateCoverage(t *testing.T) {
	// DetachContext with nil
	bg := DetachContext(nil)
	if bg == nil {
		t.Error("expected non-nil background context")
	}

	// DetachedContext methods
	ctxWithVal := context.WithValue(context.Background(), "myKey", "myVal")
	detached := DetachContext(ctxWithVal)

	if _, ok := detached.Deadline(); ok {
		t.Error("detached context should have no deadline")
	}
	if detached.Done() != nil {
		t.Error("detached context Done() should be nil")
	}
	if detached.Err() != nil {
		t.Error("detached context Err() should be nil")
	}
	if detached.Value("myKey") != "myVal" {
		t.Errorf("expected myVal, got %v", detached.Value("myKey"))
	}
}

func TestAuthUltimateCoverage(t *testing.T) {
	secret := "test-secret-key-1234"

	// 1. VerifyJWT errors
	_, err := VerifyJWT("malformed-token", secret)
	if err == nil {
		t.Error("expected error for malformed token")
	}

	// Invalid signature
	badSigToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjMifQ.bad-signature"
	_, err = VerifyJWT(badSigToken, secret)
	if err == nil {
		t.Error("expected error for bad signature")
	}

	// Bad payload encoding
	badPayloadToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.!!!not-base64.signature"
	_, err = VerifyJWT(badPayloadToken, secret)
	if err == nil {
		t.Error("expected error for bad payload encoding")
	}

	// Expired token
	expiredClaims := JWTClaims{
		Subject:   "usr-expired",
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
	}
	expiredToken, _ := GenerateJWT(expiredClaims, secret)
	_, err = VerifyJWT(expiredToken, secret)
	if err == nil || err.Error() != "token has expired" {
		t.Errorf("expected token has expired error, got: %v", err)
	}

	// 2. Context.User with non-user or nil
	c := &Context{}
	if c.User() != nil {
		t.Error("expected nil for unauthenticated user")
	}
	c.Set(userContextKey, "string-not-user")
	if c.User() != nil {
		t.Error("expected nil for non-*User context value")
	}

	// 3. RequireRoles failure
	app := New(Config{})
	app.GET("/admin-only", RequireRoles("admin"), func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Unauthenticated
	w1 := httptest.NewRecorder()
	app.ServeHTTP(w1, httptest.NewRequest("GET", "/admin-only", nil))
	if w1.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w1.Code)
	}

	// Authenticated without required role
	app.GET("/role-test", func(c *Context) error {
		c.SetUser(&User{ID: "u1", Roles: []string{"viewer"}})
		return c.Next()
	}, RequireRoles("admin", "manager"), func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})
	w2 := httptest.NewRecorder()
	app.ServeHTTP(w2, httptest.NewRequest("GET", "/role-test", nil))
	if w2.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w2.Code)
	}
}

func TestBloomUltimateCoverage(t *testing.T) {
	// Zero values and edge conditions
	bf1 := NewBloomFilter(0, 0)
	if bf1 == nil {
		t.Error("expected non-nil bloom filter")
	}
	bf2 := NewBloomFilter(1, 0.999)
	if bf2 == nil {
		t.Error("expected non-nil bloom filter")
	}
}

func TestDatabaseUltimateCoverage(t *testing.T) {
	// WithPrimary / IsPrimaryRequired nil checks
	pCtx := WithPrimary(nil)
	if !IsPrimaryRequired(pCtx) {
		t.Error("expected IsPrimaryRequired(WithPrimary(nil)) to be true")
	}
	if IsPrimaryRequired(nil) {
		t.Error("expected false for IsPrimaryRequired(nil)")
	}

	// openDBPool invalid driver
	_, err := NewDatabase(DBConfig{Driver: "unsupported_driver", DSN: "invalid"})
	if err == nil {
		t.Error("expected error for unsupported driver")
	}

	// ExecOne 0 rows affected
	db, err := NewDatabase(DBConfig{Driver: DBSQLite, DSN: "file::memory:?cache=shared&mode=rwc"})
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	_, _ = db.Exec("CREATE TABLE t (id INT);")
	err = db.ExecOne(ctx, "UPDATE t SET id = 2 WHERE id = 999;")
	if !errors.Is(err, ErrNotOneRowAffected) {
		t.Errorf("expected ErrNotOneRowAffected, got %v", err)
	}
}
