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

.PHONY: poc-install poc-login poc-inspect poc-clean-tools poc-clean test

poc-install:
	$(POC) -install

poc-login:
	$(POC) login

poc-inspect:
	$(POC) inspect

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
