package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A browser app is an inference client: /v1 answers its preflight and reflects its origin,
// while the token guard behind it still decides. Control routes are unchanged.
func TestInferenceCORS(t *testing.T) {
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusUnauthorized) // stands in for the token guard
	})
	h := guardOrigin(nil, next) // /v1 never reads the app's settings

	// the preflight carries no token, so it is answered here and never reaches the guard
	pre := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	pre.Header.Set("Origin", "https://naklios.dev")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	pre.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	pre.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d, want 204", rec.Code)
	}
	for k, want := range map[string]string{
		"Access-Control-Allow-Origin":          "https://naklios.dev",
		"Access-Control-Allow-Headers":         "Authorization, Content-Type, X-Api-Key",
		"Access-Control-Allow-Private-Network": "true",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("preflight %s = %q, want %q", k, got, want)
		}
	}
	if reached != 0 {
		t.Fatalf("the preflight reached the token guard")
	}

	// the real request goes through to the guard, with the origin reflected so the page can read the answer
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://naklios.dev")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached != 1 || rec.Code != http.StatusUnauthorized {
		t.Fatalf("request: reached=%d code=%d, want the guard's 401", reached, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://naklios.dev" {
		t.Errorf("request ACAO = %q", got)
	}

	// an SDK sends no Origin: nothing added
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("no Origin, yet CORS headers were set")
	}
}

// The control surface is not widened: a cross-origin page still cannot reach /api.
func TestControlRoutesStaySameOrigin(t *testing.T) {
	h := guardOrigin(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("reached a control route cross-origin") }))
	req := httptest.NewRequest(http.MethodGet, "/api/usage", nil)
	req.Header.Set("Origin", "https://naklios.dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("control route cross-origin: %d, ACAO %q — want 403 and none", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
