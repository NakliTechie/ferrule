// Package startup is Ferrule's answer to "be running when I get here".
//
// A household router nobody started is a household router nobody uses: the family opens
// an app, it cannot reach the endpoint, and the person who set it up gets asked. So the
// daemon can be registered with the login manager — and, just as importantly, can say
// whether it is registered and take itself back out.
//
// The mechanism is per-platform and deliberately not abstracted beyond three questions,
// because there is nothing shared between a launchd plist and a systemd user unit worth
// pretending is one thing.
package startup

// State is what the login manager currently says about Ferrule.
type State struct {
	// Supported reports whether this build knows how to register on this platform. False
	// is an honest answer, not a failure: the caller says so rather than silently doing
	// nothing and letting a switch sit in a position it does not hold.
	Supported bool `json:"supported"`
	// Enabled reports whether Ferrule is registered to start at login.
	Enabled bool `json:"enabled"`
	// Path is where the registration lives, for a person who wants to look at it.
	Path string `json:"path,omitempty"`
	// Unattended reports whether the daemon can actually open the vault without a person
	// at the keyboard. A passphrase vault cannot, and registering it to start at login
	// would produce a login item that fails every morning.
	Unattended bool `json:"unattended"`
	// Reason explains a false in a sentence a person can act on.
	Reason string `json:"reason,omitempty"`
}
