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
BIN_DIR := ~/Dexter/bin
SERVICE_NAME := dex-event-service
TEST_HANDLER := event-test-handler
TRANSCRIPTION_HANDLER := event-transcription-handler
PUBLIC_MESSAGE_HANDLER := event-public-message-handler
PRIVATE_MESSAGE_HANDLER := event-private-message-handler
WEBHOOK_HANDLER := event-webhook-handler
GREETING_HANDLER := event-greeting-handler

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

.PHONY: all service handlers clean install build deps check format lint test test-handler transcription-handler public-message-handler private-message-handler webhook-handler greeting-handler

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
handlers: test-handler transcription-handler public-message-handler private-message-handler webhook-handler greeting-handler

# Build test handler
test-handler:
	@echo "Building $(TEST_HANDLER)..."
	@$(GOBUILD) $(GOFLAGS) -o $(TEST_HANDLER) ./handlers/test
	@echo "✓ $(TEST_HANDLER) built successfully"

# Build transcription handler
transcription-handler:
	@echo "Building $(TRANSCRIPTION_HANDLER)..."
	@$(GOBUILD) $(GOFLAGS) -o $(TRANSCRIPTION_HANDLER) ./handlers/transcription
	@echo "✓ $(TRANSCRIPTION_HANDLER) built successfully"

# Build public message handler
public-message-handler:
	@echo "Building $(PUBLIC_MESSAGE_HANDLER)..."
	@$(GOBUILD) $(GOFLAGS) -o $(PUBLIC_MESSAGE_HANDLER) ./handlers/publicmessage
	@echo "✓ $(PUBLIC_MESSAGE_HANDLER) built successfully"

# Build private message handler
private-message-handler:
	@echo "Building $(PRIVATE_MESSAGE_HANDLER)..."
	@$(GOBUILD) $(GOFLAGS) -o $(PRIVATE_MESSAGE_HANDLER) ./handlers/privatemessage
	@echo "✓ $(PRIVATE_MESSAGE_HANDLER) built successfully"

# Build webhook handler
webhook-handler:
	@echo "Building $(WEBHOOK_HANDLER)..."
	@$(GOBUILD) $(GOFLAGS) -o $(WEBHOOK_HANDLER) ./handlers/webhook
	@echo "✓ $(WEBHOOK_HANDLER) built successfully"

# Build greeting handler
greeting-handler:
	@echo "Building $(GREETING_HANDLER)..."
	@$(GOBUILD) $(GOFLAGS) -o $(GREETING_HANDLER) ./handlers/greeting
	@echo "✓ $(GREETING_HANDLER) built successfully"

# Install binaries to Dexter bin directory
install: all
	@echo "Installing binaries to $(BIN_DIR)..."
	@mkdir -p $(BIN_DIR)
	@cp $(SERVICE_NAME) $(BIN_DIR)/$(SERVICE_NAME)
	@cp $(TEST_HANDLER) $(BIN_DIR)/$(TEST_HANDLER)
	@cp $(TRANSCRIPTION_HANDLER) $(BIN_DIR)/$(TRANSCRIPTION_HANDLER)
	@cp $(PUBLIC_MESSAGE_HANDLER) $(BIN_DIR)/$(PUBLIC_MESSAGE_HANDLER)
	@cp $(PRIVATE_MESSAGE_HANDLER) $(BIN_DIR)/$(PRIVATE_MESSAGE_HANDLER)
	@cp $(WEBHOOK_HANDLER) $(BIN_DIR)/$(WEBHOOK_HANDLER)
	@cp $(GREETING_HANDLER) $(BIN_DIR)/$(GREETING_HANDLER)
	@chmod +x $(BIN_DIR)/$(SERVICE_NAME)
	@chmod +x $(BIN_DIR)/$(TEST_HANDLER)
	@chmod +x $(BIN_DIR)/$(TRANSCRIPTION_HANDLER)
	@chmod +x $(BIN_DIR)/$(PUBLIC_MESSAGE_HANDLER)
	@chmod +x $(BIN_DIR)/$(PRIVATE_MESSAGE_HANDLER)
	@chmod +x $(BIN_DIR)/$(WEBHOOK_HANDLER)
	@chmod +x $(BIN_DIR)/$(GREETING_HANDLER)
	@echo "✓ Installed $(SERVICE_NAME) to $(BIN_DIR)"
	@echo "✓ Installed $(TEST_HANDLER) to $(BIN_DIR)"
	@echo "✓ Installed $(TRANSCRIPTION_HANDLER) to $(BIN_DIR)"
	@echo "✓ Installed $(PUBLIC_MESSAGE_HANDLER) to $(BIN_DIR)"
	@echo "✓ Installed $(PRIVATE_MESSAGE_HANDLER) to $(BIN_DIR)"
	@echo "✓ Installed $(WEBHOOK_HANDLER) to $(BIN_DIR)"
	@echo "✓ Installed $(GREETING_HANDLER) to $(BIN_DIR)"
	@rm -f $(SERVICE_NAME)
	@rm -f $(TEST_HANDLER)
	@rm -f $(TRANSCRIPTION_HANDLER)
	@rm -f $(PUBLIC_MESSAGE_HANDLER)
	@rm -f $(PRIVATE_MESSAGE_HANDLER)
	@rm -f $(WEBHOOK_HANDLER)
	@rm -f $(GREETING_HANDLER)
	@echo "✓ Cleaned source directory"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -f $(SERVICE_NAME)
	@rm -f $(TEST_HANDLER)
	@rm -f $(TRANSCRIPTION_HANDLER)
	@rm -f $(PUBLIC_MESSAGE_HANDLER)
	@rm -f $(PRIVATE_MESSAGE_HANDLER)
	@rm -f $(WEBHOOK_HANDLER)
	@rm -f $(GREETING_HANDLER)
	@rm -f $(BIN_DIR)/$(SERVICE_NAME)
	@rm -f $(BIN_DIR)/$(TEST_HANDLER)
	@rm -f $(BIN_DIR)/$(TRANSCRIPTION_HANDLER)
	@rm -f $(BIN_DIR)/$(PUBLIC_MESSAGE_HANDLER)
	@rm -f $(BIN_DIR)/$(PRIVATE_MESSAGE_HANDLER)
	@rm -f $(BIN_DIR)/$(WEBHOOK_HANDLER)
	@rm -f $(BIN_DIR)/$(GREETING_HANDLER)
	@echo "✓ Clean complete"
