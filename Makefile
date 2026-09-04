BINARY  := ferrule
VERSION ?= dev
LDFLAGS := -s -w -X main.Version=$(VERSION)
TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64

.PHONY: build check test vet fmt dist clean run demo sync-llms app app-universal

build: sync-llms
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ferrule

# llms.txt lives at the repo root for anyone reading the repository, and is embedded so a
# running daemon can serve it at /llms.txt. go:embed cannot reach above its own directory,
# so the copy is made here and a test fails if the two ever drift.
sync-llms:
	@cp llms.txt internal/ui/llms.txt

# The gate. `done` is this command's word, not a self-report.
# NOT `sync-llms` first. Synchronising the embedded copy before running the test that
# checks for drift makes that test incapable of failing here — a gate that repairs the
# thing it is about to inspect is not a gate. `make sync-llms` is the explicit fix.
check: fmt vet test

fmt:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files"; exit 1)

vet:
	go vet ./...

# A skipped checkpoint is not a passed checkpoint. `go test` exits 0 on a skip and says
# so only in verbose output, so the gate reads the verbose output and refuses any skip.
# A machine that cannot run a gate must say so loudly, not quietly accept the package.
test:
	@set -o pipefail; go test ./... -v 2>&1 | tee /tmp/ferrule-test.out | grep -E "^(ok|FAIL|---)" || true; 	  if grep -q "^--- SKIP" /tmp/ferrule-test.out; then 	    echo; echo "a checkpoint was skipped, which the gate does not accept:"; 	    grep -A2 "^--- SKIP" /tmp/ferrule-test.out; exit 1; 	  fi; 	  if grep -q "^--- FAIL\|^FAIL" /tmp/ferrule-test.out; then exit 1; fi

run: build
	./$(BINARY) serve

# A whole Ferrule with fake providers, so it can be evaluated end to end without owning
# a single API key. Not built by `dist`; it is a development tool, not the product.
demo:
	go run ./cmd/ferrule-demo

# One statically-linked binary per target, no cgo, no runtime dependencies.
dist: sync-llms
	@rm -rf dist && mkdir -p dist
	@for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
	  echo "  $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
	    -o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/ferrule || exit 1; \
	done
	@cd dist && shasum -a 256 * > SHA256SUMS && cat SHA256SUMS

# Ferrule.app — a double-clickable wrapper around `ferrule open`, for the household this
# is actually for. No new dependency: the icon is committed as .icns, the launcher is
# three lines of shell, and the binary inside is the same one `make build` produces. The
# bundle is self-contained, so it can be dragged to Applications or anywhere else.
# One bundle that runs on both kinds of Mac. Go cannot emit a fat binary, so the two
# builds are joined with lipo — otherwise a download page has to ask people which
# processor they have, which is a question a household app has no business asking.
app-universal:
	@mkdir -p dist
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/.ferrule-arm64 ./cmd/ferrule
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/.ferrule-amd64 ./cmd/ferrule
	@lipo -create -output dist/.ferrule-universal dist/.ferrule-arm64 dist/.ferrule-amd64
	@rm -f dist/.ferrule-arm64 dist/.ferrule-amd64
	@$(MAKE) --no-print-directory app BINARY_FOR_APP=dist/.ferrule-universal
	@rm -f dist/.ferrule-universal
	@cd dist && ditto -c -k --keepParent Ferrule.app Ferrule-macos.zip
	@echo "dist/Ferrule-macos.zip — universal, arm64 + amd64"

app: build
	@rm -rf dist/Ferrule.app
	@mkdir -p dist/Ferrule.app/Contents/MacOS dist/Ferrule.app/Contents/Resources
	@sed 's/__VERSION__/$(VERSION)/g' packaging/Info.plist > dist/Ferrule.app/Contents/Info.plist
	@cp packaging/Ferrule.icns dist/Ferrule.app/Contents/Resources/Ferrule.icns
	@cp packaging/launcher.sh dist/Ferrule.app/Contents/MacOS/Ferrule
	@cp $(or $(BINARY_FOR_APP),$(BINARY)) dist/Ferrule.app/Contents/Resources/ferrule
	@chmod +x dist/Ferrule.app/Contents/MacOS/Ferrule dist/Ferrule.app/Contents/Resources/ferrule
	@printf 'APPL????' > dist/Ferrule.app/Contents/PkgInfo
	@echo "dist/Ferrule.app — drag it to Applications, or double-click it where it is."

clean:
	rm -rf dist $(BINARY)
