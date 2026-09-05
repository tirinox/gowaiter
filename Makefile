APP_NAME ?= gowaiter
BIN_DIR ?= .bin
BIN := $(BIN_DIR)/$(APP_NAME)

IMAGE ?= tirinox/gowaiter
CONTAINER ?= gowaiter_instance
PORT ?= 10025
DATA_VOLUME ?= gowaiter_data
CRON_CONFIG ?= example.cron.json

GO ?= go
GOTOOLCHAIN ?= auto
GO_CMD := GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO)

.PHONY: help all build run deps tidy fmt fmt-check test test-race vet vuln check ci \
	docker-build docker-run clean

help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Development:"
	@echo "  deps          Download Go module dependencies"
	@echo "  tidy          Update go.mod and go.sum"
	@echo "  fmt           Format Go source files"
	@echo "  fmt-check     Check Go source formatting"
	@echo "  build         Build the binary into $(BIN)"
	@echo "  run           Run the service locally"
	@echo "  clean         Remove local build output"
	@echo ""
	@echo "Checks:"
	@echo "  test          Run tests"
	@echo "  test-race     Run tests with the race detector"
	@echo "  vet           Run go vet"
	@echo "  vuln          Check for known vulnerabilities"
	@echo "  check         Run formatting, vet, and tests"
	@echo "  ci            Run all CI checks"
	@echo "  all           Run checks and build the binary"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build  Build the Docker image ($(IMAGE))"
	@echo "  docker-run    Run the Docker image on port $(PORT) with persistent timer data"

all: check build

deps:
	$(GO_CMD) mod download

tidy:
	$(GO_CMD) mod tidy

fmt:
	$(GO_CMD) fmt ./...

fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "The following files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi

test:
	$(GO_CMD) test ./...

test-race:
	$(GO_CMD) test -race ./...

vet:
	$(GO_CMD) vet ./...

vuln:
	$(GO_CMD) run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt-check vet test

ci: check test-race vuln

build:
	mkdir -p $(BIN_DIR)
	$(GO_CMD) build -trimpath -o $(BIN) .

run:
	$(GO_CMD) run . -cron-config $(CRON_CONFIG)

docker-build:
	docker build --pull -t $(IMAGE) .

docker-run:
	docker run --rm --name $(CONTAINER) -p $(PORT):10025 \
		-v $(DATA_VOLUME):/data \
		-v "$(abspath $(CRON_CONFIG)):/app/cron.json:ro" \
		$(IMAGE) -cron-config /app/cron.json

clean:
	$(RM) -r $(BIN_DIR)
