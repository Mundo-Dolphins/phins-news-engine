COVERAGE_FILE := coverage.out
# Minimum total statement coverage: max(measured - 1, 70). See
# docs/EXTRACTION-NOTES.md for the figure it was derived from.
COVERAGE_THRESHOLD := 81.4

# Override with `make test RACE_FLAG=` to skip the race detector.
RACE_FLAG ?= -race

# golangci-lint version used by CI (.github/workflows/ci.yml).
GOLANGCI_LINT_VERSION := v2.14.0

.PHONY: help build vet test cover lint validate-config docs-check smoke release-script-test ci

help:
	@echo "make build            Compile every package"
	@echo "make vet              Run go vet"
	@echo "make test             Run the test suite (race detector on)"
	@echo "make cover            Run the tests (race) with coverage and enforce COVERAGE_THRESHOLD"
	@echo "make lint             Run golangci-lint with .golangci.yml"
	@echo "make validate-config  Load every file in configs/example through the engine loaders"
	@echo "make docs-check       Validate README.md and docs/ (links, paths, commands, YAML)"
	@echo "make smoke            Build a throwaway host module against this library"
	@echo "make ci               Everything CI runs (except the boundary check)"

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./... $(RACE_FLAG) -count=1

cover:
	go test ./... $(RACE_FLAG) -count=1 -covermode=atomic -coverprofile=$(COVERAGE_FILE)
	@go tool cover -func=$(COVERAGE_FILE) | awk '/^total:/ {print $$3}' | sed 's/%//' | \
		LC_ALL=C awk -v min=$(COVERAGE_THRESHOLD) '{ \
			if ($$1+0 < min) { printf "coverage %.1f%% is below the required %.1f%%\n", $$1, min; exit 1 } \
			else { printf "coverage %.1f%% meets the required %.1f%%\n", $$1, min } \
		}'

lint:
	golangci-lint run ./...

validate-config:
	go test ./configs/example -run TestExampleConfigs -count=1

docs-check:
	go test ./docscheck -count=1

smoke:
	tools/smoke/consumer.sh

release-script-test:
	.github/scripts/test-next-patch-release.sh

ci: release-script-test build vet lint cover docs-check validate-config smoke
