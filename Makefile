GO ?= go
PYTHON ?= python3
ACTIONLINT_VERSION := v1.7.12
VERSION ?= 0.1.0-dev
REVISION := $(shell git rev-parse HEAD)
BUILT_AT := $(shell git show -s --format=%cI HEAD)
LDFLAGS := -X main.version=$(VERSION) -X main.revision=$(REVISION) -X main.builtAt=$(BUILT_AT)

.PHONY: deps tools build test test-component lint

deps:
	$(GO) mod download all
	$(GO) mod verify
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/fleeting-plugin-aliyun ./cmd/fleeting-plugin-aliyun
test:
	$(GO) test ./...
test-component:
	$(GO) test -count=1 -timeout=5m ./tests/component
tools:
	GOBIN=$(CURDIR)/.cache/tools $(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
lint: tools
	@test -z "$$($(GO) fmt ./...)"
	$(GO) vet ./...
	.cache/tools/actionlint
	$(PYTHON) tools/check-configs.py
	git diff --check
