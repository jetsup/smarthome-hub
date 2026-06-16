# Program Name
BINARY_NAME=smarthome-hub

# Build directories
BUILD_DIR=build

# Versioning (pulls from git tag if available)
VERSION=$(shell git describe --tags --always 2>/dev/null || echo "v0.1.0")

.PHONY: all help clean local pi-arm64 linux-amd64 windows-amd64 macos-arm64 release

# Default target when running just 'make'
all: help

help:
	@echo "Available build targets for $(BINARY_NAME):"
	@echo "  make local          - Build for your current machine's OS/architecture"
	@echo "  make pi-arm64       - Build for Raspberry Pi 5 (Linux ARM64)"
	@echo "  make linux-amd64    - Build for standard Linux servers (AMD64)"
	@echo "  make windows-amd64  - Build for Windows desktop (64-bit)"
	@echo "  make macos-arm64    - Build for Apple Silicon Macs (M1/M2/M3/M4)"
	@echo "  make release        - Build for ALL platforms simultaneously with unique names"
	@echo "  make clean          - Remove all generated binaries"

## Single Platform Builds

local:
	@echo "Building for current local platform..."
	go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) main.go
	@echo "Done! Binary located at: $(BUILD_DIR)/$(BINARY_NAME)"

pi-arm64:
	@echo "Building for Raspberry Pi 5 (Linux ARM64)..."
	GOOS=linux GOARCH=arm64 go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 main.go
	@echo "Done! Binary located at: $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64"

linux-amd64:
	@echo "Building for Linux AMD64..."
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 main.go

windows-amd64:
	@echo "Building for Windows AMD64..."
	GOOS=windows GOARCH=amd64 go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe main.go

macos-arm64:
	@echo "Building for macOS ARM64..."
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 main.go

## Multi-Platform Release Target

release: clean pi-arm64 linux-amd64 windows-amd64 macos-arm64
	@echo ""
	@echo "======================================================="
	@echo "All executables built successfully in ./$(BUILD_DIR)/:"
	@ls -l $(BUILD_DIR)
	@echo "======================================================="

## Clean up binaries
clean:
	@echo "Cleaning up build artifacts..."
	rm -rf $(BUILD_DIR)
	@echo "Clean complete."
