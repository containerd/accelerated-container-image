# Development Guide for Claude

This document provides development guidance for working with the accelerated-container-image project.

## Building

### Production Build (Linux)
The project is designed to run on Linux. Use the Makefile for production builds:

```bash
# Build all binaries
make binaries

# Build specific binary
make bin/overlaybd-snapshotter

# Clean build artifacts
make clean
```

### Development Build (macOS)
For development and testing on macOS, you can build with Linux target:

```bash
# Build for Linux from macOS
GOOS=linux go build -o bin/overlaybd-snapshotter ./cmd/overlaybd-snapshotter/

# Build other components
GOOS=linux go build -o bin/ctr ./cmd/ctr/
GOOS=linux go build -o bin/convertor ./cmd/convertor/
```

**Note**: The binaries won't run on macOS due to Linux-specific dependencies (overlayFS, disk quotas, etc.), but this is useful for compilation testing and development.

## Testing

### Unit Tests
```bash
# Run tests (requires root on Linux)
make test

# Run specific package tests
go test ./pkg/snapshot/...
go test ./internal/log/...
```

### Syntax Validation
```bash
# Check syntax without building
go list ./cmd/... ./pkg/... ./internal/...

# Format code
go fmt ./...

# Vet code
go vet ./...
```

## Code Formatting

The project uses standard Go formatting:

```bash
# Format all Go files
go fmt ./...

# Check formatting
gofmt -l .

# Fix imports
go mod tidy
```

## Linting

```bash
# Run go vet
go vet ./...

# Check for common issues
go mod verify
```

## Platform-Specific Notes

### Linux-Only Components
- `pkg/snapshot/diskquota/` - Project quota management
- OverlayFS mounting functionality
- Block device operations

### Cross-Platform Components
- gRPC service interfaces
- Logging and metrics
- Configuration parsing
- Basic snapshot logic

## Development Workflow

1. **Make changes** to Go files
2. **Test compilation**: `GOOS=linux go build ./cmd/overlaybd-snapshotter/`
3. **Format code**: `go fmt ./...`
4. **Validate**: `go vet ./...`
5. **Test on Linux** (if available): `make test`

## Package Structure

```
cmd/
├── overlaybd-snapshotter/  # Main snapshotter service
├── ctr/                    # Container runtime tool
└── convertor/              # Image conversion tool

pkg/
├── snapshot/               # Core snapshotter implementation
├── metrics/                # Prometheus metrics
└── utils/                  # Utility functions

internal/
└── log/                    # Internal logging utilities
```

## Tips

- **macOS Development**: Use `GOOS=linux` for all builds
- **Testing**: Full testing requires Linux environment
- **Dependencies**: Run `go mod tidy` after adding new imports
- **Build Errors**: Check build constraints (`//go:build linux`) if compilation fails
