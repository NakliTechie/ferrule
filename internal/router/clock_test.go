package router

import (
	"net/http"
	"testing"

	"github.com/NakliTechie/ferrule/internal/store"
)

// A cloud provider held one real request for 196 seconds and then answered 500 with an
// empty body. There was no bound of Ferrule's own to cut that short, and the bound that
// exists now must not be a cloud provider's clock applied to a local runtime, whose first
// token legitimately waits on a model loading into memory.
func TestTheWaitForAFirstByteIsBoundedAndByKind(t *testing.T) {
	r := New(nil, nil)
	firstByte := func(c *http.Client) (bool, string) {
		tr, ok := c.Transport.(*http.Transport)
		if !ok {
			return false, "not an *http.Transport"
		}
		return tr.ResponseHeaderTimeout > 0, tr.ResponseHeaderTimeout.String()
	}

	cloud := r.clientFor(store.Source{Kind: store.KindCloud})
	local := r.clientFor(store.Source{Kind: store.KindLocal})
	if bounded, d := firstByte(cloud); !bounded {
		t.Errorf("a cloud upstream has no deadline for its first byte: %s", d)
	}
	if bounded, d := firstByte(local); !bounded {
		t.Errorf("a local runtime has no deadline for its first byte: %s", d)
	}
	if localFirstByte <= cloudFirstByte {
		t.Errorf("a local runtime is held to %v, no longer than a cloud provider's %v",
			localFirstByte, cloudFirstByte)
	}
	if cloud == local {
		t.Error("both kinds share one client, so they share one clock")
	}
	// The body must stay unbounded: a long generation is a legitimate request.
	if cloud.Timeout != 0 || local.Timeout != 0 {
		t.Error("an overall client timeout would cut a long generation short")
	}
}
