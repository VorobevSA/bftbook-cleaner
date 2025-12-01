.PHONY: build install deps tidy

# Binary name
BINARY_NAME=bftbook-cleaner

# Download dependencies
deps:
	go mod download

# Clean up and update dependencies
tidy:
	go mod tidy

# Build the binary
build: deps
	go build -o $(BINARY_NAME) .

# Install the binary to GOPATH/bin
install: deps
	go install .

