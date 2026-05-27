# jess Makefile

GO ?= go

# go-licenses version is pinned for reproducible audits. Bump deliberately.
GO_LICENSES ?= github.com/google/go-licenses@v1.6.0

# License types go-licenses must reject. AGPL classifies as "forbidden" and
# GPL/LGPL as "restricted"; both violate the project policy (MIT/Apache-2.0/
# MPL-2.0/BSD only). MPL-2.0 classifies as "reciprocal" and is allowed, so it
# is deliberately left out of this list.
DISALLOWED_LICENSE_TYPES ?= forbidden,restricted

# golangci-lint version, pinned to match CI (.github/workflows/test.yml).
# Bump both together.
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT_PKG ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: vet test lint license-audit

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

# lint runs golangci-lint against .golangci.yml. Prefers an installed
# binary (fast); falls back to `go run` so a fresh clone needs only the
# Go toolchain.
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		$(GO) run $(GOLANGCI_LINT_PKG) run; \
	fi

# license-audit fails if any dependency carries a disallowed license type.
# Uses `go run` so contributors need nothing installed beyond the Go toolchain.
license-audit:
	$(GO) run $(GO_LICENSES) check ./... --disallowed_types=$(DISALLOWED_LICENSE_TYPES)
