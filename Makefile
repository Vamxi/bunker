BINARY  := bunker
PWD     := $(shell pwd)
PKG     := ./...
# Releases pass the version explicitly: make release VERSION=0.2.0
VERSION ?= 0.0.0
LDFLAGS  = -ldflags="-X main.Version=$(VERSION)"

# Formatting, tests, and binaries share files even with make -j.
.NOTPARALLEL:

export PATH := $(PATH):$(shell go env GOPATH)/bin

# -race also enables checkptr, which rejects the uintptr→pointer conversions
# in gotk4's generated marshallers. Keep checkptr on for bunker's own code.
RACEFLAGS := -race -gcflags='github.com/diamondburned/gotk4/pkg/...=-d=checkptr=0'
BENCHSTAT := go run golang.org/x/perf/cmd/benchstat@latest

# GUI tests run on a private headless compositor when mutter is installed,
# so they work with the screen locked and never touch the desktop session.
HEADLESS := $(if $(shell command -v mutter 2>/dev/null),dbus-run-session -- ./scripts/headless-gui.sh)

# Everything, including the GUI integration tests. See TESTING.md.
test:
	$(HEADLESS) go test $(PKG) github.com/gdamore/tcell/v2/... -count=1 $(RACEFLAGS)

# Only the GUI integration tests.
test-gui:
	$(HEADLESS) go test . -run 'TestGUI_' -count=1 -v

# Benchmarks, compared with the committed baseline when there is one.
bench:
	mkdir -p bench
	$(HEADLESS) go test ./... -run '^$$' -bench . -benchmem -count 6 | tee bench/latest.txt
	@if [ -f bench/baseline.txt ]; then $(BENCHSTAT) bench/baseline.txt bench/latest.txt; fi

# Accept the latest results as the new baseline (commit bench/baseline.txt).
bench-baseline:
	cp bench/latest.txt bench/baseline.txt

vet:
	go vet $(PKG)

fmt:
	@command -v goimports >/dev/null 2>&1 || { \
	  echo "goimports is not installed. Install it with:"; \
	  echo "  go install golang.org/x/tools/cmd/goimports@latest"; \
	  exit 1; \
	}
	goimports -w $$(find . -name '*.go' -not -path './third_party/*')

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint is not installed. Install it with:"; \
	  echo "  grm install golangci/golangci-lint"; \
	  exit 1; \
	}
	golangci-lint run

check: fmt vet build lint
	@echo "==> make check: all green"

standards:
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.universal.md \
	    -o AGENTS.universal.md
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.go.md \
	    -o AGENTS.go.md

# bunker links GTK4 through cgo, so it builds natively for linux/amd64 only
# (needs gtk4-devel). The first build compiles the GTK bindings (~9 min);
# later builds are cached.
bin/$(BINARY): bin/$(BINARY)_linux_amd64
	cp $< $@
	ln -sf bin/$(BINARY) $(BINARY)
bin/$(BINARY)_linux_amd64: test
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $@

build: bin/$(BINARY)

release-version:
	@case "$(VERSION)" in 0.0.0) echo "usage: make release VERSION=x.y.z"; exit 1;; esac
	@if gh release view "v$(VERSION)" -R Vamxi/$(BINARY) >/dev/null 2>&1; then echo "v$(VERSION) is already released"; exit 1; fi

release: release-version build
	tar -czf bin/$(BINARY)_linux_amd64.tar.gz --transform 's|.*/$(BINARY)_.*|$(BINARY)|' bin/$(BINARY)_linux_amd64
	GITHUB_TOKEN="$${GITHUB_TOKEN:-$$(gh auth token)}" grm release Vamxi/$(BINARY) \
		-f bin/$(BINARY)_linux_amd64.tar.gz \
		-t "v$(VERSION)"

run: test
	go build -o $(BINARY) .
	./$(BINARY) --trace

# Install the binary to ~/.local/bin and add bunker to the app grid.
install: build
	install -Dm755 bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	$(HOME)/.local/bin/$(BINARY) install-desktop

clean:
	rm -rf bin/ $(BINARY)

.PHONY: build release release-version install test test-gui bench bench-baseline vet fmt lint check standards run clean
