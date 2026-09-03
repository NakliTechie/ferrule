package ui_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"ferrule/internal/ui"
)

// llms.txt exists twice: at the repo root, where an agent pointed at the repository
// finds it, and inside this package, where go:embed can reach it so a running daemon can
// serve it at /llms.txt. go:embed cannot look above its own directory and does not follow
// symlinks, so a copy is the only option — and a copy without a check is a file that
// drifts. This is the check.
func TestEmbeddedLLMsMatchesTheRepoRoot(t *testing.T) {
	embedded, err := ui.Asset("../llms.txt")
	if err != nil {
		// Asset reads from the assets/ subtree; the embed lives beside it.
		embedded, err = os.ReadFile("llms.txt")
		if err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.ReadFile(filepath.Join("..", "..", "llms.txt"))
	if err != nil {
		t.Fatalf("the repo root has no llms.txt: %v", err)
	}
	if !bytes.Equal(embedded, root) {
		t.Fatal("internal/ui/llms.txt has drifted from the repo root copy — run `make sync-llms`")
	}
}
