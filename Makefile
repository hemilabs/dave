# Copyright (c) 2025-2026 Hemi Labs, Inc.
# Use of this source code is governed by the MIT License,
# which can be found in the LICENSE file.

PROJECTPATH := $(abspath $(dir $(realpath $(firstword $(MAKEFILE_LIST)))))
PROJECT_BIN := $(PROJECTPATH)/bin

export COCACHE ?= $(shell go env GOCACHE)
export GOBIN ?= $(shell go env GOPATH)/bin

# renovate: datasource=github-releases depName=golangci/golangci-lint versioning=semver
GOLANGCI_LINT_VERSION := v2.12.2
# renovate: datasource=github-releases depName=joshuasing/golicenser versioning=semver
GOLICENSER_VERSION := v0.3.1
# renovate: datasource=go depName=golang.org/x/vuln versioning=semver
GOVULNCHECK_VERSION := v1.6.0

.PHONY: all
all: tidy build lint test

.PHONY: clean
clean:
	rm -rf $(PROJECT_BIN)

.PHONY: deps
deps: lint-deps go-deps

.PHONY: go-deps
go-deps:
	go mod download
	go mod verify

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: build
build:
	go build -trimpath -ldflags "-s -w $(GO_LDFLAGS)" -o $(PROJECT_BIN)/dave $(PROJECTPATH)/cmd/dave

.PHONY: install
install:
	go install -trimpath -ldflags "-s -w $(GO_LDFLAGS)" $(PROJECTPATH)/cmd/dave

.PHONY: test
test:
	go test -timeout=20m -coverprofile=$(PROJECTPATH)/coverage.out \
 		-covermode=atomic -ldflags "$(GO_LDFLAGS)" ./...

.PHONY: race
race:
	go test -race -timeout=20m -coverprofile=$(PROJECTPATH)/coverage.out \
 		-covermode=atomic -ldflags "$(GO_LDFLAGS)" ./...

.PHONY: cover
cover: test
	go tool cover -html=$(PROJECTPATH)/coverage.out

define LICENSE_HEADER
Copyright (c) {{.year}} {{.author}}
Use of this source code is governed by the MIT License,
which can be found in the LICENSE file.
endef
export LICENSE_HEADER
LICENSE_AUTHOR := Hemi Labs, Inc.

.PHONY: fmt
fmt:
	$(GOBIN)/golangci-lint fmt ./...
	$(GOBIN)/golicenser -tmpl="$$LICENSE_HEADER" -author="$(LICENSE_AUTHOR)" -year-mode=git-range -fix ./...

.PHONY: lint
lint: fmt
	$(GOBIN)/golangci-lint run --fix ./...

.PHONY: fmt-check
fmt-check:
	$(GOBIN)/golangci-lint fmt --diff ./...
	$(GOBIN)/golicenser -tmpl="$$LICENSE_HEADER" -author="$(LICENSE_AUTHOR)" -year-mode=git-range ./...

.PHONY: lint-check
lint-check:
	$(GOBIN)/golangci-lint run ./...

.PHONY: check
check: fmt-check lint-check

.PHONY: lint-deps
lint-deps:
	@echo "Installing with $(shell go version)"
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/joshuasing/golicenser/cmd/golicenser@$(GOLICENSER_VERSION)

.PHONY: vulncheck
vulncheck:
	$(GOBIN)/govulncheck ./...

.PHONY: vulncheck-deps
vulncheck-deps:
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
