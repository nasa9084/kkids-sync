# Everything the PoC downloads stays under .poc/ so it can be removed in one go:
# the Playwright driver (Node.js + playwright-core), Chromium, and the Go module
# and build caches used to build it.
POC_DIR := $(CURDIR)/.poc

export PLAYWRIGHT_DRIVER_PATH  := $(POC_DIR)/tools/playwright-driver
export PLAYWRIGHT_BROWSERS_PATH := $(POC_DIR)/tools/playwright-browsers
export GOMODCACHE := $(POC_DIR)/tools/go-mod
export GOCACHE    := $(POC_DIR)/tools/go-build
export GOPATH     := $(POC_DIR)/tools/go-path

POC := go run ./cmd/poc -dir $(POC_DIR)

.PHONY: poc-install poc-login poc-inspect poc-inspect-write reconciler-login reconciler-sync reconciler-sync-full poc-clean-tools poc-clean test

poc-install:
	$(POC) -install

poc-login:
	$(POC) login

poc-inspect:
	$(POC) inspect

# Like poc-inspect, but toggles really change what is shared. Each write is
# saved to .poc/write-*.txt. Turn back anything you change.
poc-inspect-write:
	$(POC) -allow-writes inspect

# The reconciler shares the PoC's tools and session for now.
RECONCILER := go run ./cmd/kkids-sync-reconciler -state $(POC_DIR)/storage-state.json

reconciler-login:
	$(RECONCILER) login

# Read-only: writes the library and share state to .poc/snapshot.json.
reconciler-sync:
	$(RECONCILER) -out $(POC_DIR)/snapshot.json sync

# Scroll through the whole library, e.g. to notice books that left it.
reconciler-sync-full:
	$(RECONCILER) -out $(POC_DIR)/snapshot.json -full sync

# Remove the downloaded tools and caches, keep session and recordings.
poc-clean-tools:
	@if [ -d "$(POC_DIR)/tools" ]; then chmod -R u+w "$(POC_DIR)/tools"; rm -rf "$(POC_DIR)/tools"; fi
	@echo "removed $(POC_DIR)/tools"

# Remove everything the PoC created, including the saved session (cookies).
poc-clean:
	@if [ -d "$(POC_DIR)" ]; then chmod -R u+w "$(POC_DIR)"; rm -rf "$(POC_DIR)"; fi
	@echo "removed $(POC_DIR)"

test:
	go vet ./...
	go test ./...
