BIN := ouro
PREFIX ?= $(HOME)/.local
# "ouro update" pulls from the repository the binary was built from.
OURO_REPO ?= $(shell git remote get-url origin 2>/dev/null)
# The tool "rebuild" builds the checkout the binary was built from. "ouro update"
# builds in a directory of its own and passes on the checkout of the old binary.
OURO_SOURCE ?= $(CURDIR)
LDFLAGS := -s -w $(if $(OURO_REPO),-X ouroboros/internal/update.Repo=$(OURO_REPO)) \
	$(if $(OURO_SOURCE),-X 'ouroboros/internal/update.Source=$(OURO_SOURCE)')

# Go is often installed outside PATH (install.sh puts it into ~/.local/go).
# A go that is already in PATH wins.
export PATH := $(PATH):$(or $(OURO_GO_DIR),$(HOME)/.local/go)/bin:/usr/local/go/bin

.PHONY: build test cover install clean \
	check fmt-check vet lint cover-check mutation tools hooks unhooks

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o "$(BIN)" ./cmd/ouro

vet:
	go vet ./...

test: vet
	go test -race ./...

# Coverage including the end-to-end test, which runs the real binary. The unit
# tests and that binary write into one directory; cover.out is both merged.
cover:
	rm -rf .cover cover.out && mkdir .cover
	OURO_E2E_COVERDIR=$(CURDIR)/.cover go test -count=1 -cover -coverpkg=ouroboros/... ./... -args -test.gocoverdir=$(CURDIR)/.cover >.cover/test.log 2>&1 || { cat .cover/test.log; exit 1; }
	go tool covdata textfmt -i=.cover -o=cover.out
	go tool covdata percent -i=.cover

# Everything the pre-commit hook runs, on the working tree. See "Checks" in the README.
check: fmt-check lint test cover-check mutation

# Is every Go file formatted? Changes no file.
fmt-check:
	@scripts/fmt-check.sh

# staticcheck, pinned in scripts/tool-versions, configured in staticcheck.conf.
lint:
	@staticcheck=$$(scripts/tool.sh staticcheck) && "$$staticcheck" ./... && echo "lint: ok"

# 100% statement coverage of the production code; exclusions are in coverage-exclusions.txt.
cover-check: cover
	@scripts/cover-check.sh cover.out

# Mutation tests of the whole module, or of some packages: make mutation PKGS="internal/a2a internal/agent"
mutation:
	@scripts/mutation.sh $(PKGS)

# Build the pinned check tools into .tools/ (done on first use anyway).
tools:
	@scripts/tool.sh staticcheck >/dev/null
	@scripts/tool.sh gremlins >/dev/null

# Install the pre-commit hook into this checkout, or remove it again.
hooks:
	@scripts/install-hooks.sh $(if $(CHAIN),--chain)

unhooks:
	@scripts/install-hooks.sh --uninstall

install: build
	mkdir -p "$(PREFIX)/bin"
	install -m755 "$(BIN)" "$(PREFIX)/bin/$(notdir $(BIN))"

clean:
	rm -rf $(BIN) .cover cover.out .mutation
