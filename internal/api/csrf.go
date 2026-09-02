package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

// The control plane's threat model is a web page the person happens to have open.
//
// Origin checking alone does not close it. A browser omits `Origin` entirely on
// `<img src>`, `<script src>`, stylesheet loads, and top-level navigations, so a guard
// that lets no-Origin requests through lets those through — and if any of them can reach
// a mutating operation, a page the person merely visited can drive this daemon. That is
// exactly what happened here before this file existed: an `<img>` tag pointed at
// `/api/op/set_setting?key=cross_origin&value=on` flipped the developer setting, after
// which any origin could mint itself an app token.
//
// Two things close it, and both are needed:
//
//  1. A per-run secret the panel is handed when the daemon serves it the page. A
//     cross-origin caller cannot read that page, so it cannot obtain the secret. Every
//     control call must present it.
//  2. Mutations are POST only. A `<form>` can POST cross-origin without an Origin-free
//     escape hatch, but it cannot set a custom header — so (1) covers it — and refusing
//     GET removes the entire class of URL-only attacks.

// Token is the per-run control-plane secret. It lives in memory, changes on every
// restart, and is never written to disk.
type Token struct{ value string }

// NewToken mints the run's control token.
func NewToken() (*Token, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &Token{value: base64.RawURLEncoding.EncodeToString(b)}, nil
}

// Value returns the token, for embedding in the panel the daemon itself serves.
func (t *Token) Value() string { return t.value }

// Valid reports whether a request carries the run's control token.
func (t *Token) Valid(r *http.Request) bool {
	got := r.Header.Get("X-Ferrule-Control")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(t.value)) == 1
}

// HeaderName is the header the panel sends the control token in. A custom header cannot
// be set by a cross-origin form or an image load, which is the point.
const HeaderName = "X-Ferrule-Control"

// LocalOrigin reports whether an Origin header names this machine.
//
// It parses rather than prefix-matches: `http://localhost.evil.example` starts with
// "http://localhost" and is not this machine.
func LocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}
