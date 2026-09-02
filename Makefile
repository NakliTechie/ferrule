BINARY  := ferrule
VERSION ?= dev
LDFLAGS := -s -w -X main.Version=$(VERSION)
TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64

.PHONY: build check test vet fmt dist clean run

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ferrule

# The gate. `done` is this command's word, not a self-report.
check: fmt vet test

fmt:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files"; exit 1)

vet:
	go vet ./...

test:
	go test ./...

run: build
	./$(BINARY) serve

# One statically-linked binary per target, no cgo, no runtime dependencies.
dist:
	@rm -rf dist && mkdir -p dist
	@for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
	  echo "  $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
	    -o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/ferrule || exit 1; \
	done
	@cd dist && shasum -a 256 * > SHA256SUMS && cat SHA256SUMS

clean:
	rm -rf dist $(BINARY)
