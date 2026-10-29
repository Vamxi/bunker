BINARY  := bunker
# Releases pass the version explicitly: make release VERSION=0.7.0
VERSION ?= 0.0.0
RELEASE := target/release/$(BINARY)
RPM      = target/$(BINARY)-$(VERSION)-1.x86_64.rpm

# nfpm installs with go install, into ~/go/bin.
export PATH := $(PATH):$(HOME)/go/bin

# Formatting, tests, and binaries share files even with make -j.
.NOTPARALLEL:

# A release build names its version; a development build the crate's.
BUILD_ENV := $(if $(filter-out 0.0.0,$(VERSION)),BUNKER_VERSION=$(VERSION))

RUST_TOOLS := Install them with: sudo dnf install rustfmt clippy

# Format, test, build, lint: run after every change.
check: fmt test build lint
	@echo "==> make check: all green"

fmt:
	@command -v rustfmt >/dev/null 2>&1 || { echo "rustfmt is not installed. $(RUST_TOOLS)"; exit 1; }
	cargo fmt --all

test:
	cargo test --workspace

build:
	$(BUILD_ENV) cargo build --release -p $(BINARY)
	@echo "==> $(RELEASE)"

lint:
	@command -v cargo-clippy >/dev/null 2>&1 || { echo "clippy is not installed. $(RUST_TOOLS)"; exit 1; }
	cargo clippy --workspace --all-targets -- -D warnings

# Emulator throughput on the benchmark workloads (no window).
bench:
	cargo run --release -q -p bunker-vt --example workloads -- target/workloads
	cargo run --release -q -p bunker-vt --example throughput -- target/workloads

# The same workloads in real windows: bunker, Ptyxis, foot (scripts/termbench.sh).
termbench: build
	scripts/termbench.sh

run: build
	$(RELEASE) --trace

# Install the binary to ~/.local/bin and add bunker to the app grid.
install: build
	install -Dm755 $(RELEASE) $(HOME)/.local/bin/$(BINARY)
	$(HOME)/.local/bin/$(BINARY) install-desktop

release-version:
	@case "$(VERSION)" in 0.0.0) echo "usage: make release VERSION=x.y.z"; exit 1;; esac
	@if gh release view "v$(VERSION)" -R Vamxi/$(BINARY) >/dev/null 2>&1; then echo "v$(VERSION) is already released"; exit 1; fi
	@grep -q '^## $(VERSION)$$' CHANGELOG.md || { echo "CHANGELOG.md has no '## $(VERSION)' section"; exit 1; }

# One package with the binary, launcher, and icons (see nfpm.yaml).
rpm: build
	@command -v nfpm >/dev/null 2>&1 || { \
	  echo "nfpm is not installed. Install it with:"; \
	  echo "  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest"; \
	  exit 1; \
	}
	rm -rf target/pkgroot
	$(RELEASE) install-desktop --data-dir target/pkgroot/usr/share --exec /usr/bin/$(BINARY)
	VERSION=$(VERSION) nfpm package --config nfpm.yaml --packager rpm --target $(RPM)

# The release carries only the RPM: grm installs it with dnf.
release: release-version test rpm
	GITHUB_TOKEN="$${GITHUB_TOKEN:-$$(gh auth token)}" grm release Vamxi/$(BINARY) \
		-f $(RPM) \
		-t "v$(VERSION)"
	awk '/^## /{p = ($$2 == "$(VERSION)"); next} p' CHANGELOG.md > target/notes.md
	gh release edit "v$(VERSION)" -R Vamxi/$(BINARY) --notes-file target/notes.md

standards:
	curl -sL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.universal.md \
	    -o AGENTS.universal.md

clean:
	cargo clean

.PHONY: check fmt test build lint bench termbench run install release-version rpm release standards clean
