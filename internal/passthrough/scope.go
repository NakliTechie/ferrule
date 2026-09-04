package passthrough

import (
	"net/http"
	"strings"
)

// The passthrough mount attaches the person's provider key to whatever comes through it.
// That is the deal for a media source whose request shape cannot be normalised — and it
// has to be the deal for *inference on that source only*.
//
// Without a scope, a Ferrule app token is general authority over the whole provider
// account behind the base URL: an app granted "make me an image" could also list files,
// delete a fine-tune, or read billing. A grant is meant to be an app's identity, not a
// bearer credential for everything the key can reach.
//
// So each passthrough provider declares the surface it actually needs. Anything else is
// refused before the key is fetched from the vault, and the person is told they can make
// that call directly — Ferrule is declining to lend the key, not declining the operation.
// scope is the set of routes inference legitimately touches on a provider, and the
// methods each of them allows.
//
// Methods used to be one set covering every prefix, so allowing POST for predictions also
// allowed POST to models and collections — which on Replicate creates a model. The comment
// said those paths were for reading metadata; the code did not.
type scope map[string]map[string]bool

var scopes = map[string]scope{
	// Replicate's inference surface: create a prediction, poll it, cancel it, and read
	// the model metadata needed to build one. Not /account, not /trainings, not
	// /deployments, not /files, not /webhooks.
	"replicate": {
		// Creating and polling a prediction is the whole point.
		"predictions": {http.MethodGet: true, http.MethodPost: true},
		// Metadata only. POST here creates a model on the person's account.
		"models":      {http.MethodGet: true},
		"collections": {http.MethodGet: true},
	},
}

// canonical normalises the path a caller asked for, and refuses anything that could mean
// one path here and a different path at the provider.
//
// This is the whole guard. Checking only the first segment let `predictions/../account`
// through: the check saw `predictions` and said inference, and the tail was then joined
// onto the base URL and sent with the key attached. Ferrule answered the caller 403, and
// the provider — which normalises before routing — served /account. An app token became a
// credential for the account itself, which is exactly what this scope exists to prevent.
//
// The tail arrives percent-decoded from net/http, so `%2e%2e` is already `..` here.
func canonical(tail string) (string, bool) {
	if tail == "" || strings.ContainsAny(tail, "\\\x00") {
		return "", false
	}
	segs := strings.Split(tail, "/")
	for _, s := range segs {
		// An empty segment collapses upstream, and a dot segment climbs. Neither is a
		// path a caller can have a legitimate reason to ask for here.
		if s == "" || s == "." || s == ".." {
			return "", false
		}
	}
	return strings.Join(segs, "/"), true
}

// allowed reports whether a request is inference on this provider. A provider with no
// declared scope is refused entirely rather than allowed by default — a new passthrough
// provider must state its surface before a key is lent to it.
//
// It returns the canonical path to forward, so the caller cannot send one thing to the
// check and another to the provider.
func allowed(provider, method, tail string) (string, bool) {
	sc, ok := scopes[provider]
	if !ok {
		return "", false
	}
	clean, ok := canonical(tail)
	if !ok {
		return "", false
	}
	head, _, _ := strings.Cut(clean, "/")
	methods, ok := sc[head]
	if !ok || !methods[method] {
		return "", false
	}
	return clean, true
}
