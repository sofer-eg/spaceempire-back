package auth_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"spaceempire/back/internal/auth"
)

func newTestServer(t *testing.T) (*auth.Server, *http.ServeMux) {
	t.Helper()
	svc := auth.NewService(newStubRepo(), fixedClock{t: time.Now()}, nil, auth.ServiceConfig{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	srv := auth.NewServer(svc, auth.ServerConfig{
		CookieSecure:      false,
		SessionTTLSeconds: 3600,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	return srv, mux
}

func doJSON(t *testing.T, mux http.Handler, method, path string, body any, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf := new(bytes.Buffer)
		_ = json.NewEncoder(buf).Encode(body)
		rdr = buf
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Result()
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatal("session cookie not set")
	return nil
}

func TestUnit_Handler_Register_SetsCookie(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)

	resp := doJSON(t, mux, http.MethodPost, "/api/auth/register", auth.RegisterRequest{Login: "sofer", Password: "1", Race: 1})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	c := sessionCookie(t, resp)
	if !c.HttpOnly {
		t.Fatal("cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Fatalf("Path = %q, want /", c.Path)
	}
	if c.MaxAge != 3600 {
		t.Fatalf("MaxAge = %d, want 3600", c.MaxAge)
	}

	var body auth.PlayerResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PlayerID == 0 {
		t.Fatal("PlayerID = 0")
	}
}

func TestUnit_Handler_Register_DuplicateReturns409(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)
	resp := doJSON(t, mux, http.MethodPost, "/api/auth/register", auth.RegisterRequest{Login: "sofer", Password: "1", Race: 1})
	_ = resp.Body.Close()

	resp2 := doJSON(t, mux, http.MethodPost, "/api/auth/register", auth.RegisterRequest{Login: "sofer", Password: "1", Race: 1})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp2.StatusCode)
	}
}

func TestUnit_Handler_Register_InvalidJSONReturns400(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUnit_Handler_Register_EmptyLoginReturns400(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)
	resp := doJSON(t, mux, http.MethodPost, "/api/auth/register", auth.RegisterRequest{Login: "", Password: "1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnit_Handler_Login_BadCredentialsReturns401(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)

	resp := doJSON(t, mux, http.MethodPost, "/api/auth/login", auth.LoginRequest{Login: "ghost", Password: "1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUnit_Handler_FullCycle_RegisterLoginMeLogout(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)

	// register
	r1 := doJSON(t, mux, http.MethodPost, "/api/auth/register", auth.RegisterRequest{Login: "sofer", Password: "1", Race: 1})
	_ = r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d", r1.StatusCode)
	}
	regCookie := sessionCookie(t, r1)

	// /me with the register cookie
	r2 := doJSON(t, mux, http.MethodGet, "/api/auth/me", nil, regCookie)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("/me status = %d", r2.StatusCode)
	}
	var me auth.PlayerResponse
	if err := json.NewDecoder(r2.Body).Decode(&me); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if me.Login != "sofer" {
		t.Fatalf("me.Login = %q, want sofer", me.Login)
	}

	// login → new cookie
	r3 := doJSON(t, mux, http.MethodPost, "/api/auth/login", auth.LoginRequest{Login: "sofer", Password: "1"})
	_ = r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", r3.StatusCode)
	}
	loginCookie := sessionCookie(t, r3)
	if loginCookie.Value == regCookie.Value {
		t.Fatal("login produced same token as register; tokens must be unique per call")
	}

	// logout invalidates the login token
	r4 := doJSON(t, mux, http.MethodPost, "/api/auth/logout", nil, loginCookie)
	_ = r4.Body.Close()
	if r4.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", r4.StatusCode)
	}

	// /me with the now-invalid cookie → 401
	r5 := doJSON(t, mux, http.MethodGet, "/api/auth/me", nil, loginCookie)
	defer r5.Body.Close()
	if r5.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout /me status = %d, want 401", r5.StatusCode)
	}
}

func TestUnit_Handler_Me_NoCookieReturns401(t *testing.T) {
	t.Parallel()

	_, mux := newTestServer(t)
	resp := doJSON(t, mux, http.MethodGet, "/api/auth/me", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUnit_Middleware_RequireAuth_NoCookieReturns401(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t)
	protected := srv.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUnit_Middleware_RequireAuth_ValidCookiePropagatesPlayerID(t *testing.T) {
	t.Parallel()

	srv, mux := newTestServer(t)

	r1 := doJSON(t, mux, http.MethodPost, "/api/auth/register", auth.RegisterRequest{Login: "sofer", Password: "1", Race: 1})
	_ = r1.Body.Close()
	regCookie := sessionCookie(t, r1)

	var seenPlayerID int64
	protected := srv.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid, ok := auth.PlayerIDFromContext(r.Context())
		if !ok {
			t.Error("PlayerIDFromContext returned ok=false")
		}
		seenPlayerID = int64(pid)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(regCookie)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if seenPlayerID == 0 {
		t.Fatal("PlayerID was not propagated to handler context")
	}
}
