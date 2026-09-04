// Package provider holds the curated seed set (§4.4). Breadth is the anti-goal: widen
// only when a real request arrives. Everything here is data about how to talk to a
// source — no keys, no state.
package provider

import (
	"net/http"
	"sort"
	"strings"

	"github.com/NakliTechie/ferrule/internal/store"
)

// Listing names the model-listing dialect a source speaks.
const (
	ListOpenAI    = "openai"
	ListOllama    = "ollama"
	ListReplicate = "replicate"
)

// Spec describes one provider: where it lives, how it authenticates, how it lists.
type Spec struct {
	ID             string
	Label          string
	Kind           string // store.KindLocal | store.KindCloud
	Lane           string // store.LaneTokens | store.LanePassthrough
	DefaultBaseURL string
	NeedsKey       bool
	NeedsBaseURL   bool
	AuthHeader     string
	AuthPrefix     string
	ExtraHeaders   map[string]string
	Listing        string
	// DetectURLs are probed verbatim during local detection.
	DetectURLs []string
	// KeyHint is the shape of the provider's key, shown in the paste field.
	KeyHint string
	// Docs points a person at where to mint the key.
	Docs string
}

var specs = map[string]Spec{
	"ollama": {
		ID: "ollama", Label: "Ollama", Kind: store.KindLocal, Lane: store.LaneTokens,
		DefaultBaseURL: "http://127.0.0.1:11434/v1", Listing: ListOpenAI,
		DetectURLs: []string{"http://127.0.0.1:11434/v1"},
	},
	"lmstudio": {
		ID: "lmstudio", Label: "LM Studio", Kind: store.KindLocal, Lane: store.LaneTokens,
		DefaultBaseURL: "http://127.0.0.1:1234/v1", Listing: ListOpenAI,
		DetectURLs: []string{"http://127.0.0.1:1234/v1"},
	},
	"llamacpp": {
		ID: "llamacpp", Label: "llama.cpp", Kind: store.KindLocal, Lane: store.LaneTokens,
		DefaultBaseURL: "http://127.0.0.1:8080/v1", Listing: ListOpenAI,
		DetectURLs: []string{"http://127.0.0.1:8080/v1", "http://127.0.0.1:8081/v1"},
	},
	"anthropic": {
		ID: "anthropic", Label: "Anthropic", Kind: store.KindCloud, Lane: store.LaneTokens,
		DefaultBaseURL: "https://api.anthropic.com/v1", NeedsKey: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		ExtraHeaders: map[string]string{"anthropic-version": "2023-06-01"},
		Listing:      ListOpenAI, KeyHint: "sk-ant-…",
		Docs: "https://console.anthropic.com/settings/keys",
	},
	"deepseek": {
		ID: "deepseek", Label: "DeepSeek", Kind: store.KindCloud, Lane: store.LaneTokens,
		DefaultBaseURL: "https://api.deepseek.com/v1", NeedsKey: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ", Listing: ListOpenAI,
		KeyHint: "sk-…", Docs: "https://platform.deepseek.com/api_keys",
	},
	"groq": {
		ID: "groq", Label: "Groq", Kind: store.KindCloud, Lane: store.LaneTokens,
		DefaultBaseURL: "https://api.groq.com/openai/v1", NeedsKey: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ", Listing: ListOpenAI,
		KeyHint: "gsk_…", Docs: "https://console.groq.com/keys",
	},
	"openai": {
		ID: "openai", Label: "OpenAI", Kind: store.KindCloud, Lane: store.LaneTokens,
		DefaultBaseURL: "https://api.openai.com/v1", NeedsKey: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ", Listing: ListOpenAI,
		KeyHint: "sk-…", Docs: "https://platform.openai.com/api-keys",
	},
	// NVIDIA's hosted NIM endpoints. OpenAI-compatible for chat and listing, which is why
	// it costs nothing to seed: added on demand, per §4.4, not on spec.
	"nvidia": {
		ID: "nvidia", Label: "NVIDIA", Kind: store.KindCloud, Lane: store.LaneTokens,
		DefaultBaseURL: "https://integrate.api.nvidia.com/v1", NeedsKey: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ", Listing: ListOpenAI,
		KeyHint: "nvapi-…", Docs: "https://build.nvidia.com",
	},
	"openai-compatible": {
		ID: "openai-compatible", Label: "OpenAI-compatible", Kind: store.KindCloud,
		Lane: store.LaneTokens, NeedsKey: false, NeedsBaseURL: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ", Listing: ListOpenAI,
		KeyHint: "optional",
	},
	"replicate": {
		ID: "replicate", Label: "Replicate", Kind: store.KindCloud, Lane: store.LanePassthrough,
		DefaultBaseURL: "https://api.replicate.com/v1", NeedsKey: true,
		AuthHeader: "Authorization", AuthPrefix: "Bearer ", Listing: ListReplicate,
		KeyHint: "r8_…", Docs: "https://replicate.com/account/api-tokens",
	},
}

// Get returns the spec for id.
func Get(id string) (Spec, bool) { s, ok := specs[id]; return s, ok }

// All returns every spec, sorted by id.
func All() []Spec {
	out := make([]Spec, 0, len(specs))
	for _, s := range specs {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Local returns the specs that local detection scans for.
func Local() []Spec {
	var out []Spec
	for _, s := range All() {
		if s.Kind == store.KindLocal {
			out = append(out, s)
		}
	}
	return out
}

// Names lists known provider ids for error messages.
func Names() string {
	ids := make([]string, 0, len(specs))
	for id := range specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// Authorize stamps a request with the source's credentials. The key arrives from the
// vault at call time and is never held anywhere else.
func (s Spec) Authorize(req *http.Request, key string) {
	if key != "" {
		h := s.AuthHeader
		if h == "" {
			h = "Authorization"
		}
		p := s.AuthPrefix
		if p == "" {
			p = "Bearer "
		}
		req.Header.Set(h, p+key)
		// Anthropic accepts either; send both so listing and chat share one path.
		if s.ID == "anthropic" {
			req.Header.Set("x-api-key", key)
		}
	}
	for k, v := range s.ExtraHeaders {
		req.Header.Set(k, v)
	}
}

// URL joins a path onto the source's base URL.
func URL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}
