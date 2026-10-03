BIN := ouro
PREFIX ?= $(HOME)/.local
# "ouro update" pulls from the repository the binary was built from.
OURO_REPO ?= $(shell git remote get-url origin 2>/dev/null)
LDFLAGS := -s -w $(if $(OURO_REPO),-X ouroboros/internal/update.Repo=$(OURO_REPO))

.PHONY: build test cover install clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/ouro

test:
	go vet ./...
	go test -race ./...

# Coverage including the end-to-end test, which runs the real binary.
cover:
	rm -rf .cover && mkdir .cover
	OURO_E2E_COVERDIR=$(CURDIR)/.cover go test -count=1 -cover -coverpkg=ouroboros/... ./... -args -test.gocoverdir=$(CURDIR)/.cover >/dev/null
	go tool covdata percent -i=.cover

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)

clean:
	rm -rf $(BIN) .cover cover.out
