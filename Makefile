# Makefile for dex-event-service
# Builds the main service and all handlers

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOFMT=$(GOCMD) fmt
GOLINT=golangci-lint

# Paths
BIN_DIR := /home/owen/Dexter/bin
SERVICE_NAME := dex-event-service
HANDLER_TEST := event-test-handler

# Build information
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "0.0.0")
BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git rev-parse --short HEAD)
BUILD_DATE := $(shell date -u +"%Y-%m-%d-%H-%M-%S")
BUILD_YEAR := $(shell date -u +"%Y")
BUILD_ARCH := linux-amd64
BUILD_HASH := $(shell cat /dev/urandom | tr -dc 'a-z0-9' | fold -w 8 | head -n 1)

# Go build flags (compatible with dex-cli build system)
GOFLAGS := -ldflags="-s -w -X main.version=$(VERSION) -X main.branch=$(BRANCH) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE) -X main.buildYear=$(BUILD_YEAR) -X main.buildHash=$(BUILD_HASH) -X main.arch=$(BUILD_ARCH)"

.PHONY: all service handlers clean install build deps check format lint test test-handler

# Ensure modules are downloaded and go.sum is correct
deps:
	@echo "Ensuring Go modules are tidy..."
	@$(GOCMD) mod tidy

check: deps format lint test

format:
	@echo "Formatting..."
	@$(GOFMT) ./...

lint:
	@echo "Linting..."
	@$(GOLINT) run

test:
	@echo "Testing..."
	@$(GOTEST) -v ./...

# Default target: build everything
all: service handlers

# Build the main service
service: check
	@echo "Building $(SERVICE_NAME)..."
	@$(GOBUILD) $(GOFLAGS) -o $(SERVICE_NAME) .
	@echo "✓ $(SERVICE_NAME) built successfully"

# Build all handlers
handlers: test-handler

# Build test handler
test-handler:
	@echo "Building $(HANDLER_TEST)..."
	@$(GOBUILD) $(GOFLAGS) -o $(HANDLER_TEST) ./handlers/test
	@echo "✓ $(HANDLER_TEST) built successfully"

# Install binaries to Dexter bin directory
install: all
	@echo "Installing binaries to $(BIN_DIR)..."
	@mkdir -p $(BIN_DIR)
	@cp $(SERVICE_NAME) $(BIN_DIR)/$(SERVICE_NAME)
	@cp $(HANDLER_TEST) $(BIN_DIR)/$(HANDLER_TEST)
	@chmod +x $(BIN_DIR)/$(SERVICE_NAME)
	@chmod +x $(BIN_DIR)/$(HANDLER_TEST)
	@echo "✓ Installed $(SERVICE_NAME) to $(BIN_DIR)"
	@echo "✓ Installed $(HANDLER_TEST) to $(BIN_DIR)"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -f $(SERVICE_NAME)
	@rm -f $(HANDLER_TEST)
	@rm -f $(BIN_DIR)/$(SERVICE_NAME)
	@rm -f $(BIN_DIR)/$(HANDLER_TEST)
	@echo "✓ Clean complete"

# Build and install in one step
build: clean all install
	@echo "✓ Build complete - $(SERVICE_NAME) v$(VERSION) ready!"
