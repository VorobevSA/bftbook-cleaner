# Contributing to BFTbook Cleaner

Thank you for your interest in contributing to BFTbook Cleaner! This document provides guidelines and instructions for contributing.

## Prerequisites

- Go 1.25 or newer
- Basic understanding of Go programming language
- Git

## Getting Started

1. Fork the repository
2. Clone your fork:
   ```bash
   git clone https://github.com/your-username/CometBFT-Addrbook-Cleaner.git
   cd CometBFT-Addrbook-Cleaner
   ```
3. Install dependencies:
   ```bash
   go mod download
   ```

## Development Workflow

1. Create a new branch for your changes:
   ```bash
   git checkout -b feature/your-feature-name
   ```
2. Make your changes
3. Ensure your code follows Go best practices and passes all checks (see below)
4. Commit your changes with clear, descriptive commit messages
5. Push to your fork and create a Pull Request

## Code Quality Checks

Before submitting a Pull Request, ensure your code passes all checks:

### Formatting

```bash
# Format code
gofmt -s -w .

# Organize imports
goimports -w .
```

### Linting

```bash
# Run golangci-lint
golangci-lint run --timeout=5m
```

### Security

```bash
# Check for vulnerabilities
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

### Build and Test

```bash
# Ensure code compiles
go build ./...

# Run go vet
go vet ./...

# Clean up dependencies
go mod tidy
```

## Code Standards

- Follow Go best practices and idiomatic Go code style
- Write clear, self-documenting code with comments where necessary
- Keep functions focused and small
- Handle errors explicitly (no ignored error returns)
- Use meaningful variable and function names
- Add comments for exported functions and types

## Pull Request Process

1. Ensure all checks pass locally
2. Update documentation if needed (README.md, etc.)
3. Write a clear description of your changes
4. Reference any related issues
5. Wait for code review and address feedback

## Questions?

If you have questions or need help, please open an issue for discussion.

Thank you for contributing!

