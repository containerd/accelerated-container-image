# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.

## Project Overview

Accelerated Container Image is an implementation of DADI (Data Accelerator for Disaggregated Infrastructure) for container acceleration. It provides overlaybd, a block-device-based remote image format that enables on-demand image fetching without downloading entire images.

The project consists of:
- **overlaybd-snapshotter**: A containerd snapshotter plugin for overlaybd images (compatible with OCI images)
- **convertor**: Standalone userspace tool to convert OCI images to overlaybd format
- **ctr**: Modified containerd CLI tool with overlaybd support
- **dadi-snapshotter**: Legacy snapshotter implementation (separate subdirectory with own go.mod)

## Build Commands

Build all binaries:
```bash
make
```

Build specific binaries (generates to `bin/` directory):
```bash
make binaries
```

Install binaries to system:
```bash
make install
# Installs to /opt/overlaybd/snapshotter
# Config to /etc/overlaybd-snapshotter
```

Clean build artifacts:
```bash
make clean
```

## Testing

Run tests (requires root):
```bash
go test ${GO_TESTFLAGS} ${GO_PACKAGES} -test.root
```

Or via Makefile:
```bash
make test
```

## Linting

Lint with golangci-lint (configuration in .golangci.yml):
```bash
golangci-lint run
```

Enabled linters: misspell, unconvert, staticcheck (with custom checks), gofmt, goimports

## Architecture

### Storage Types

The snapshotter handles multiple storage types (pkg/snapshot/overlay.go):
- **storageTypeNormal**: Standard OCI tar.gz layers (overlayfs lowerdir)
- **storageTypeLocalBlock**: Overlaybd format layers
- **storageTypeRemoteBlock**: Remote layers with on-demand pulling
- **storageTypeLocalLayer**: Tar file layers requiring overlaybd-turboOCI meta generation

### Key Components

**pkg/snapshot/**: Core snapshotter implementation
- overlay.go: Main snapshotter logic with storage type handling
- storage.go: Snapshot storage management

**pkg/convertor/**: Image conversion logic

**pkg/label/**: Label handling for image metadata

**pkg/types/**: Common type definitions

**cmd/overlaybd-snapshotter/**: Snapshotter service entry point
- Default config: /etc/overlaybd-snapshotter/config.json
- Exposes gRPC API via unix socket

**cmd/convertor/**: Standalone converter CLI
- Uses cobra for CLI parsing
- Supports MySQL database backend for layer deduplication
- Multiple output formats: overlaybd, turboOCI, fastoci

**cmd/ctr/**: Modified containerd ctr CLI with overlaybd support

### Filesystem Support

Overlaybd outputs virtual block devices that can be formatted with multiple filesystems (ext4, xfs, etc.). The device is exposed through TCMU (TCM userspace).

### Image Flow

1. OCI layers (tar.gz) → Overlaybd blocks + index (Zfile format)
2. Virtual block device exposed via TCMU → Filesystem mount
3. On-demand read: index lookup → remote blob fetch

## Configuration

### Snapshotter Config
Location: /etc/overlaybd-snapshotter/config.json
- `root`: Overlaybd data directory (default: /var/lib/overlaybd/)
- `address`: Unix socket path
- `rwMode`: Read-write mode setting
- `autoRemoveDev`: Auto-remove devices
- `writableLayerType`: Type for writable layers
- `asyncRemove`: Async snapshot removal

### Containerd Integration
Add to /etc/containerd/config.toml:
```toml
[proxy_plugins.overlaybd]
    type = "snapshot"
    address = "/run/overlaybd-snapshotter/overlaybd.sock"
```

## Dependencies

- Go >= 1.26.x
- containerd >= 2.0.x
- runc >= 1.0
- overlaybd backstore (https://github.com/containerd/overlaybd)

## Running Services

Start snapshotter:
```bash
sudo bin/overlaybd-snapshotter
```

Restart containerd after config changes:
```bash
sudo systemctl restart containerd
```

## Converting Images

Using convertor tool:
```bash
docker run overlaybd-convertor -r registry.hub.docker.com/library/redis -i 6.2.1 -o 6.2.1_obd_new
```

Or use the ctr rpull command for remote pull with conversion.

## Release Version Support

Images have annotation `containerd.io/snapshot/overlaybd/version`:
- `0.1.0`: All overlaybd versions
- `0.1.0-turbo.ociv1`: overlaybd >= v0.6.10

## Commit Message Style

Follow the seven rules:
1. Separate subject from body with blank line
2. Limit subject line to 50 characters
3. Capitalize subject line
4. No period at end of subject
5. Use imperative mood in subject
6. Wrap body at 72 characters
7. Explain what and why vs. how

Sign commits with:
```bash
git commit -s
```
