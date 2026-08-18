.DEFAULT_GOAL := help
.PHONY: help fmt lint test ci

GOLANGCI_VERSION := v2.12.2
TESTCOVERAGE_VERSION := v2.11.4

help:        ## Zeigt diese Hilfe
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-8s %s\n", $$1, $$2}'

fmt:         ## Quellen formatieren
	gofmt -w .

lint:        ## golangci-lint (gemeinsame Konfiguration des Oekosystems)
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) config verify
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run ./...

test:        ## Tests
	go test ./...

# Exakt das, was der Runner faehrt — siehe ADR 0008 und die Erfahrung vom
# 2026-08-18: ein `ci`-Ziel, das weniger prueft als die CI, erzeugt Vertrauen,
# das nicht gedeckt ist.
ci:          ## Alles, was auch die CI faehrt
	go vet ./...
	test -z "$$(gofmt -l .)"
	$(MAKE) lint
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go run github.com/vladopajic/go-test-coverage/v2@$(TESTCOVERAGE_VERSION) --config=./.testcoverage.yml
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go build ./...
