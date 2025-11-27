# Makefile for dex-event-service
# Builds the main service and all handlers

# Paths
BIN_DIR := /home/owen/Dexter/bin
SERVICE_NAME := dex-event-service
HANDLER_TEST := event-test-handler

# Go build flags
GOFLAGS := -ldflags="-s -w"

.PHONY: all service handlers clean install

# Default target: build everything
all: service handlers

# Build the main service
service:
	@echo "Building $(SERVICE_NAME)..."
	@go build $(GOFLAGS) -o $(SERVICE_NAME) .
	@echo "✓ $(SERVICE_NAME) built successfully"

# Build all handlers
handlers: test-handler

# Build test handler
test-handler:
	@echo "Building $(HANDLER_TEST)..."
	@go build $(GOFLAGS) -o $(HANDLER_TEST) ./handlers/test
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
	@echo "✓ Build complete - dex-event-service v1.0.0 ready!"
