.PHONY: build run test clean install lint fmt

# Binary name
BINARY=polymarket-bot
CONFIG=config.yml

# Build the bot
build:
	@echo "Building $(BINARY)..."
	go build -o $(BINARY) ./cmd/bot

# Build with optimizations for production
build-prod:
	@echo "Building $(BINARY) for production..."
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/bot

# Run the bot
run: build
	@echo "Running $(BINARY)..."
	./$(BINARY) -config $(CONFIG)

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Install dependencies
install:
	@echo "Installing dependencies..."
	go mod download
	go mod tidy

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Lint code
lint:
	@echo "Linting code..."
	golangci-lint run

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(BINARY)
	rm -f $(BINARY)-*
	rm -f coverage.out coverage.html

# Create config from example
config:
	@if [ ! -f config.yml ]; then \
		cp config.example.yml config.yml; \
		echo "Created config.yml from example"; \
	else \
		echo "config.yml already exists"; \
	fi

# Build for multiple platforms
build-all:
	@echo "Building for multiple platforms..."
	GOOS=linux GOARCH=amd64 go build -o $(BINARY)-linux-amd64 ./cmd/bot
	GOOS=linux GOARCH=arm64 go build -o $(BINARY)-linux-arm64 ./cmd/bot
	GOOS=darwin GOARCH=amd64 go build -o $(BINARY)-darwin-amd64 ./cmd/bot
	GOOS=darwin GOARCH=arm64 go build -o $(BINARY)-darwin-arm64 ./cmd/bot

# Run with race detector
run-race: build
	@echo "Running with race detector..."
	go run -race ./cmd/bot -config $(CONFIG)

# Show help
help:
	@echo "Available targets:"
	@echo "  build         - Build the bot"
	@echo "  build-prod    - Build optimized for production"
	@echo "  run           - Build and run the bot"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  install       - Install dependencies"
	@echo "  fmt           - Format code"
	@echo "  lint          - Lint code"
	@echo "  clean         - Remove build artifacts"
	@echo "  config        - Create config.yml from example"
	@echo "  build-all     - Build for multiple platforms"
	@echo "  run-race      - Run with race detector"
	@echo "  help          - Show this help message"
