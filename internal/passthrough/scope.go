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
type scope struct {
	// prefixes are path prefixes, relative to the source's base URL, that inference
	// legitimately touches.
	prefixes []string
	// methods that may be used against them.
	methods map[string]bool
}

var scopes = map[string]scope{
	// Replicate's inference surface: create a prediction, poll it, cancel it, and read
	// the model metadata needed to build one. Not /account, not /trainings, not
	// /deployments, not /files, not /webhooks.
	"replicate": {
		prefixes: []string{"predictions", "models", "collections"},
		methods:  map[string]bool{http.MethodGet: true, http.MethodPost: true},
	},
}

// allowed reports whether a request is inference on this provider. A provider with no
// declared scope is refused entirely rather than allowed by default — a new passthrough
// provider must state its surface before a key is lent to it.
func allowed(provider, method, tail string) bool {
	sc, ok := scopes[provider]
	if !ok {
		return false
	}
	if !sc.methods[method] {
		return false
	}
	head := tail
	if i := strings.IndexAny(head, "/?"); i >= 0 {
		head = head[:i]
	}
	for _, p := range sc.prefixes {
		if head == p {
			return true
		}
	}
	return false
}
