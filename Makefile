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

# Wails v2.16 uses UTType in its macOS file dialogs but does not link the
# framework that defines it.
ifeq ($(shell uname -s),Darwin)
export CGO_LDFLAGS += -framework UniformTypeIdentifiers
endif

.PHONY: build test test-gui cover install clean \
	check fmt-check vet lint cover-check mutation tools hooks unhooks

build:
	go build -tags "production,webkit2_41" -trimpath -ldflags="$(LDFLAGS)" -o "$(BIN)" ./cmd/ouro

vet:
	go vet ./...

test: vet
	go test -race ./...

# Optional native GUI checks need the desktop libraries and Node for the frontend tests.
test-gui:
	go test -race -tags "production,webkit2_41" ./internal/plugins/gui
	node --test internal/plugins/gui/frontend_test.mjs

# Coverage including the end-to-end test, which runs the real binary. The unit
# tests and that binary write into one directory; cover.out is both merged.
cover:
	rm -rf .cover cover.out && mkdir .cover
	OURO_E2E_COVERDIR=$(CURDIR)/.cover go test -count=1 -cover -coverpkg=ouroboros/... ./... -args -test.gocoverdir=$(CURDIR)/.cover >.cover/test.log 2>&1 || { cat .cover/test.log; exit 1; }
	go tool covdata textfmt -i=.cover -o=cover.out
	go tool covdata percent -i=.cover

# Every check, on the working tree: what the pre-commit hook runs, and the
# mutation tests. See "Checks" in the README.
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

# Install the pre-commit and pre-push hooks into this checkout, or remove them again.
hooks:
	@scripts/install-hooks.sh $(if $(CHAIN),--chain)

unhooks:
	@scripts/install-hooks.sh --uninstall

# basename, not notdir: notdir splits a path with spaces into words, as
# "Application Support" of the stage that rebuild installs from.
install: build
	mkdir -p "$(PREFIX)/bin"
	install -m755 "$(BIN)" "$(PREFIX)/bin/$$(basename "$(BIN)")"

clean:
	rm -rf $(BIN) .cover cover.out .mutation
