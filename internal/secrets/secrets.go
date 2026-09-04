// Package secrets holds the one shape net Ferrule uses to recognise a credential it did
// not expect to see.
//
// It was three nets, in three packages, and they had already drifted: the router's copy
// and discovery's copy matched different prefixes, and the staging check matched none of
// them because it looked at query parameter *names* instead. A security check that exists
// in three versions is a security check with two holes in it.
//
// Matching shapes is a net, not a guarantee: an opaque key matches nothing here. Callers
// that hold the literal secret redact that first and use this for what is left.
package secrets

import "regexp"

// Shape matches the forms provider keys are handed out in. Deliberately broad — a
// slightly vaguer error message costs nothing, and a key written into a database or an
// error string costs everything.
var Shape = regexp.MustCompile(
	`(?i)\b(?:sk-[A-Za-z0-9_\-]{8,}|gsk_[A-Za-z0-9_\-]{8,}|r8_[A-Za-z0-9_\-]{8,}|` +
		`nvapi-[A-Za-z0-9_\-]{8,}|xai-[A-Za-z0-9_\-]{8,}|` +
		`AIza[A-Za-z0-9_\-]{20,}|` +
		`frl_[A-Za-z0-9_\-]{8,}|Bearer\s+[A-Za-z0-9._\-]{12,}|` +
		`(?:api[-_]?key|authorization|x-api-key)["'\s:=]+[A-Za-z0-9._\-]{12,})`)

// Looks reports whether a string contains something key-shaped.
func Looks(s string) bool { return Shape.MatchString(s) }

// Redact replaces anything key-shaped with a marker.
func Redact(s string) string { return Shape.ReplaceAllString(s, "[redacted]") }
