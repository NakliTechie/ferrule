package router

import (
	"ferrule/internal/secrets"
	"strings"
)

// redactKnown removes an exact secret before falling back to shape matching.
//
// Pattern matching alone is a guess: a provider key with no recognisable prefix — an
// opaque UUID, a bare hex string — matches nothing, and an upstream that echoes the
// credential it received would have Ferrule copy it into `sources.status_reason` or
// `ledger.err`. The key is in memory at the moment the error is handled, so the honest
// redaction is to remove that exact string first and treat the patterns as a net for
// whatever else the body carries.
func redactKnown(s, secret string) string {
	if secret != "" && len(secret) >= 8 {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return redact(s)
}

// redact bounds and launders an upstream error body before it is persisted.
//
// Ferrule writes upstream failures into `sources.status_reason` and `ledger.err` so a
// person can read what actually went wrong. Those bodies are written by the provider,
// and a provider that echoes the credential it received — some do, in a debug field or a
// misconfigured gateway's 500 page — would have Ferrule copy a key straight into the one
// database that is supposed never to hold one.
func redact(s string) string {
	s = secrets.Redact(s)
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 300
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
