package discovery

import (
	"ferrule/internal/i18n"
	"ferrule/internal/provider"
)

// Code is the closed vocabulary of reasons a source does not go live.
//
// An agent driving Ferrule branches on the code and acts on the remedy; it never parses
// the message. The message is for a person, the code and the remedy are for whoever is
// deciding what to do next (DRIVER §2, §5).
type Code string

// The closed set. Adding one is a deliberate act: every code must name a distinct next
// action, or it should not exist (DRIVER §3).
const (
	CodeOK              Code = "ok"
	CodeUnreachable     Code = "unreachable"
	CodeBadKey          Code = "bad_key"
	CodeBadStatus       Code = "bad_status"
	CodeNoModels        Code = "no_models"
	CodeLocalNoModels   Code = "local_no_models"
	CodeTestFailed      Code = "test_failed"
	CodeTestTimeout     Code = "test_timeout"
	CodeUnknownProvider Code = "unknown_provider"
	CodeNeedsKey        Code = "needs_key"
	CodeNeedsBaseURL    Code = "needs_base_url"
	CodeInsecureURL     Code = "insecure_url"
	CodeRedirect        Code = "redirect"
	CodeCredentialInURL Code = "credential_in_url"
	// Account-level, key valid: the provider took the key and refused for money.
	CodeNoBalance Code = "no_balance"
	// Model-level, key valid: these particular models are not available to this account.
	CodeModelUnavailable Code = "model_unavailable"
)

// Reason is a typed outcome: what happened, what it means, and the exact next move.
type Reason struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Remedy  string `json:"remedy"`
}

// Error lets a Reason travel as an error without losing its code.
func (r Reason) Error() string { return r.Message }

// OK reports whether the reason describes a working source.
func (r Reason) OK() bool { return r.Code == CodeOK || r.Code == "" }

// message and remedy are written as an exhaustive switch rather than assembled from the
// code, so the vocabulary is closed in the compiler's eyes as well as the reader's, and
// so the string-coverage lint can see every key that ships.
func (c Code) message(detail ...any) string {
	switch c {
	case CodeOK:
		return i18n.T("reason.ok", detail...)
	case CodeUnreachable:
		return i18n.T("reason.unreachable", detail...)
	case CodeBadKey:
		return i18n.T("reason.bad_key", detail...)
	case CodeBadStatus:
		return i18n.T("reason.bad_status", detail...)
	case CodeNoModels:
		return i18n.T("reason.no_models")
	case CodeLocalNoModels:
		return i18n.T("reason.local_no_models", detail...)
	case CodeTestFailed:
		return i18n.T("reason.test_failed", detail...)
	case CodeTestTimeout:
		return i18n.T("reason.test_timeout", detail...)
	case CodeUnknownProvider:
		return i18n.T("reason.unknown_provider", detail...)
	case CodeNeedsKey:
		return i18n.T("reason.needs_key", detail...)
	case CodeNeedsBaseURL:
		return i18n.T("reason.needs_base_url")
	case CodeInsecureURL:
		return i18n.T("reason.insecure_url", detail...)
	case CodeRedirect:
		return i18n.T("reason.redirect", detail...)
	case CodeCredentialInURL:
		return i18n.T("reason.credential_in_url", detail...)
	case CodeNoBalance:
		return i18n.T("reason.no_balance", detail...)
	case CodeModelUnavailable:
		return i18n.T("reason.model_unavailable", detail...)
	}
	return string(c)
}

func (c Code) remedy() string {
	switch c {
	case CodeUnreachable:
		return i18n.T("remedy.unreachable")
	case CodeBadKey:
		return i18n.T("remedy.bad_key")
	case CodeBadStatus:
		return i18n.T("remedy.bad_status")
	case CodeNoModels:
		return i18n.T("remedy.no_models")
	case CodeLocalNoModels:
		return i18n.T("remedy.local_no_models")
	case CodeTestFailed:
		return i18n.T("remedy.test_failed")
	case CodeTestTimeout:
		return i18n.T("remedy.test_timeout")
	case CodeUnknownProvider:
		return i18n.T("remedy.unknown_provider", provider.Names())
	case CodeNeedsKey:
		return i18n.T("remedy.needs_key")
	case CodeNeedsBaseURL:
		return i18n.T("remedy.needs_base_url")
	case CodeInsecureURL:
		return i18n.T("remedy.insecure_url")
	case CodeRedirect:
		return i18n.T("remedy.redirect")
	case CodeCredentialInURL:
		return i18n.T("remedy.credential_in_url")
	case CodeNoBalance:
		return i18n.T("remedy.no_balance")
	case CodeModelUnavailable:
		return i18n.T("remedy.model_unavailable")
	}
	return ""
}

// newReason builds a typed reason with the localized message and remedy for its code.
func newReason(code Code, detail ...any) Reason {
	return Reason{Code: code, Message: code.message(detail...), Remedy: code.remedy()}
}

// localRemedy swaps in the remedy that fits a source with no key. Telling someone to
// "check the key's scope" for a keyless local runtime is worse than saying nothing.
func (r Reason) localRemedy() Reason {
	if r.Code == CodeTestFailed {
		r.Remedy = i18n.T("remedy.test_failed_local")
	}
	return r
}

// reasonf builds a reason whose message is already assembled.
func reasonf(code Code, message string) Reason {
	return Reason{Code: code, Message: message, Remedy: code.remedy()}
}

// Codes lists the closed vocabulary, for the agent contract and for tests.
func Codes() []Code {
	return []Code{CodeOK, CodeUnreachable, CodeBadKey, CodeBadStatus, CodeNoModels,
		CodeLocalNoModels, CodeTestFailed, CodeTestTimeout, CodeUnknownProvider,
		CodeNeedsKey, CodeNeedsBaseURL, CodeInsecureURL, CodeRedirect,
		CodeCredentialInURL, CodeNoBalance, CodeModelUnavailable}
}

// Code_ returns the code as a plain string, for storage.
func (r Reason) Code_() string {
	if r.Code == "" {
		return string(CodeOK)
	}
	return string(r.Code)
}

// RemedyFor exposes a code's remedy, so the contract's "every verdict names a next
// action" can be asserted rather than asked for on trust.
func RemedyFor(c Code) string { return c.remedy() }

// UnknownProvider is the typed refusal for a provider id that is not in the seed set.
// Exported so callers outside this package can return it without flattening it to a
// string — the exit code is derived from the code, so the code has to survive.
func UnknownProvider(id string) Reason { return newReason(CodeUnknownProvider, id) }
