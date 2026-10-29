GO ?= go
PYTHON ?= python3
ACTIONLINT_VERSION := $(shell $(PYTHON) -c 'import json; print(json.load(open("upstream.lock.json"))["tools"]["actionlint"])')
DIST_DIR ?= dist
VERSION ?= 0.1.0-dev
REVISION := $(shell git rev-parse HEAD)
BUILT_AT := $(shell git show -s --format=%cI HEAD)
LDFLAGS := -X main.version=$(VERSION) -X main.revision=$(REVISION) -X main.builtAt=$(BUILT_AT)

.PHONY: deps tools build test test-component lint dist test-oci live cleanup test-live-extended

deps:
	$(GO) mod download
	$(GO) mod verify
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/fleeting-plugin-aliyun ./cmd/fleeting-plugin-aliyun
test:
	$(GO) test ./...
	$(PYTHON) -m unittest discover -s tools -p 'test_*.py'
test-component:
	$(GO) test -count=1 -timeout=5m ./tests/component
tools:
	GOBIN=$(CURDIR)/.cache/tools $(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
lint: tools
	GO="$(GO)" $(PYTHON) tools/check_format.py
	$(GO) vet ./...
	.cache/tools/actionlint
	$(PYTHON) tools/check-configs.py
	git diff --check

dist:
	VERSION=$(VERSION) REVISION=$(REVISION) BUILT_AT=$(BUILT_AT) $(PYTHON) tools/dist.py
	$(MAKE) verify-dist DIST_DIR=dist
test-oci:
	$(GO) test -count=1 -timeout=5m ./tools/distribution

live: build
	$(GO) run -tags live ./cmd/aliyun-acceptance smoke
cleanup:
	$(GO) run -tags live ./cmd/aliyun-acceptance cleanup
test-live-extended:
	WORKER_SSH_KEY="$(abspath $(WORKER_SSH_KEY))" PLUGIN_BINARY="$(if $(PLUGIN_BINARY),$(abspath $(PLUGIN_BINARY)))" ACCEPTANCE_OUTPUT="$(abspath $(or $(ACCEPTANCE_OUTPUT),.live))" $(GO) test -tags live -count=1 -timeout=30m -v ./tests/live

.PHONY: security-tools lint-scripts security
security-tools:
	$(PYTHON) tools/dev_tools.py gitleaks shellcheck
	$(PYTHON) -m venv .cache/venv
	.cache/venv/bin/python -m pip install --require-hashes --only-binary=:all: -r tools/requirements-dev.txt
	GOBIN=$(CURDIR)/.cache/tools $(GO) install golang.org/x/vuln/cmd/govulncheck@$(shell $(PYTHON) -c 'import json; print(json.load(open("upstream.lock.json"))["tools"]["govulncheck"])')
lint-scripts: security-tools
	.cache/venv/bin/ruff check .
	.cache/tools/shellcheck infra/*.sh infra/fleeting-authorize-key packaging/debian/preinst packaging/debian/postinst packaging/debian/prerm packaging/debian/postrm tests/fixtures/gitlab/*.sh tools/*.sh
security: lint-scripts
	.cache/tools/gitleaks git --redact --no-banner --log-opts="$(or $(SCAN_RANGE),--all)" .
	.cache/tools/govulncheck ./...

.PHONY: verify verify-code verify-dist test-race test-responsegate test-live-offline test-configs check-toolchain check-metadata check-modules
check-toolchain:
	GO="$(GO)" $(PYTHON) tools/check_toolchain.py
check-metadata:
	$(PYTHON) tools/check_metadata.py
check-modules:
	$(GO) mod tidy -diff
test-race:
	$(GO) test -race ./...
test-responsegate:
	$(GO) test -race -tags=responsegate ./internal/acceptance ./internal/aliyun ./internal/provider
	$(GO) build -tags=responsegate -o bin/fleeting-plugin-aliyun-responsegate ./cmd/fleeting-plugin-aliyun
test-live-offline:
	$(GO) build -tags live -o bin/aliyun-acceptance ./cmd/aliyun-acceptance
	$(MAKE) test-live-extended RUN_LIVE_ACCEPTANCE= LIVE_EXTENDED_MODE=
test-configs:
	runner=$$($(PYTHON) tools/fetch-runner.py) && $(PYTHON) tools/check-configs.py --runner "$$runner"
verify-code:
	$(MAKE) check-toolchain check-metadata check-modules
	$(MAKE) deps build test lint test-race test-responsegate test-live-offline test-configs RUN_LIVE_ACCEPTANCE= LIVE_EXTENDED_MODE=
	$(MAKE) check-modules
verify: verify-code security
verify-dist:
	$(PYTHON) tools/check_dist.py "$(DIST_DIR)"
	OCI_PACKAGES="$(abspath $(DIST_DIR))/packages" VERSION="$(VERSION)" REVISION="$(REVISION)" $(GO) test -count=1 -timeout=5m -run '^TestBuiltDistributionRoundTrip$$' ./tools/distribution

.PHONY: test-debian-lifecycle
test-debian-lifecycle:
	@test "$(DISPOSABLE_HOST)" = true || (echo 'set DISPOSABLE_HOST=true only on an isolated Linux systemd host' >&2; exit 1)
	sudo $(PYTHON) tools/test-debian-lifecycle.py --disposable-host --runner "$(RUNNER_BINARY)" --packages "$(DIST_DIR)"
