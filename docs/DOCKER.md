# Docker Runtime Support

This document describes how to use overlaybd-snapshotter with Docker runtime.

## Overview

Overlaybd-snapshotter now supports Docker runtime through containerd integration. Docker differs from containerd in that it creates an extra **init layer** (identified by `-init` suffix in the key) between image layers and the container layer.

When `runtimeType` is set to `"docker"`, the snapshotter handles this init layer properly:
- **Init layer**: Attaches the overlaybd device and returns a bind mount
- **Container layer**: Returns an overlay mount combining the init layer and overlaybd mountpoint

## Prerequisites

- Docker 20.10+ with containerd image store integration
- overlaybd-snapshotter v0.6.0+
- overlaybd-tcmu service running

## Configuration

### 1. Configure overlaybd-snapshotter

Edit `/etc/overlaybd-snapshotter/config.json`:

```json
{
    "root": "/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd",
    "address": "/run/overlaybd-snapshotter/overlaybd.sock",
    "runtimeType": "docker",
    "rwMode": "overlayfs",
    "verbose": "info",
    "logReportCaller": false,
    "autoRemoveDev": true,
    "mirrorRegistry": []
}
```

**Important:**
- `runtimeType` must be set to `"docker"` (default is `"containerd"`)
- `rwMode` must be `"overlayfs"` for Docker support

### 2. Configure Docker Daemon

Enable containerd snapshotter support in Docker:

Edit `/etc/docker/daemon.json`:

```json
{
    "features": {
        "containerd-snapshotter": true
    }
}
```

Then restart Docker:

```bash
sudo systemctl restart docker
```

### 3. Configure containerd

Ensure containerd is configured to use the overlaybd snapshotter as a proxy plugin:

Edit `/etc/containerd/config.toml`:

```toml
[proxy_plugins.overlaybd]
    type = "snapshot"
    address = "/run/overlaybd-snapshotter/overlaybd.sock"
```

Restart containerd:

```bash
sudo systemctl restart containerd
```

## Usage

### Running Containers

Once configured, you can run overlaybd images with Docker:

```bash
# Pull an overlaybd image
docker pull registry.hub.docker.com/overlaybd/redis:7.2.3_obd

# Run with overlaybd snapshotter
docker run --rm -it --snapshotter=overlaybd registry.hub.docker.com/overlaybd/redis:7.2.3_obd
```

Or set overlaybd as the default snapshotter:

```bash
# In daemon.json
{
    "features": {
        "containerd-snapshotter": true
    },
    "snapshotter": "overlaybd"
}
```

### Verifying Device Creation

After starting a container, verify the overlaybd device:

```bash
# Check block devices
lsblk

# Expected output:
# NAME   MAJ:MIN RM   SIZE RO TYPE MOUNTPOINT
# sda      8:0    0   256G  1 disk /var/lib/containerd/io.containerd.snapshotter.v1.overlaybd/snapshots/xx/block/mountpoint

# Check mounts
mount | grep overlaybd
```

## How It Works

### Container Startup Flow

1. **Image Pull**: Docker pulls overlaybd format layers through containerd
2. **Init Layer Preparation**:
   - Docker creates an init layer (key ends with `-init`)
   - Snapshotter attaches overlaybd device at parent's mountpoint
   - Returns bind mount to init layer's `fs` directory
3. **Container Layer Preparation**:
   - Parent is the init layer
   - Returns overlay mount with `lowerdir=init/fs:overlaybd_mountpoint`
4. **Container Startup**: Docker starts container with the prepared rootfs

### Device Lifecycle

- **Creation**: Device is created during init layer preparation
- **Sharing**: Multiple containers from the same image share the same overlaybd device
- **Cleanup**: Device is destroyed when the init layer is removed (after all containers exit)

## Limitations

### 1. Read-Write Mode Restrictions

Docker support **only works with `rwMode: "overlayfs"`**. Other modes (`dir`, `dev`) are not supported because:
- Docker requires overlayfs for its layer management
- Init layer mechanism is designed for overlayfs-based storage

### 2. Init Layer Overhead

Docker creates an extra init layer that:
- Adds one additional layer to the overlay stack
- Consumes minimal storage (usually contains only Docker metadata)
- May add slight latency during container preparation

### 3. Device Cleanup Timing

Unlike containerd where devices are cleaned up immediately after container exit:
- Docker may delay init layer removal
- Device remains attached until init layer is garbage collected
- This is normal Docker behavior and doesn't affect functionality

### 4. Storage Compatibility

- Works with **overlaybd format images** (converted or turboOCI)
- Works with **OCI images** (converted on-the-fly, slower startup)
- Does not support:
  - Native OCI images with ZFile (use turboOCI instead)
  - Direct block device mode (`rwMode: "dev"`)

### 5. Performance Considerations

- First container startup from an image: Full overlaybd device initialization
- Subsequent containers from same image: Reuses existing device (fast)
- Image pull performance: Same as containerd mode

### 6. Known Issues

- **Device busy errors**: May occur if trying to remove an image while containers are still using it. This is handled gracefully by the snapshotter.
- **Init layer identification**: Relies on `-init` suffix in snapshot keys. Unusual Docker configurations may not be detected correctly.

## Troubleshooting

### Container fails to start

1. Check snapshotter logs:
```bash
sudo journalctl -u overlaybd-snapshotter -f
```

2. Verify runtimeType configuration:
```bash
cat /etc/overlaybd-snapshotter/config.json | grep runtimeType
```

3. Ensure overlaybd-tcmu is running:
```bash
sudo systemctl status overlaybd-tcmu
```

### Device not cleaned up

Check if containers/init layers are still holding references:
```bash
# List active snapshots
sudo ctr snapshot --snapshotter=overlaybd ls

# Check mounts
mount | grep overlaybd
```

### Performance issues

1. Enable trace recording for frequently used images
2. Check network connectivity to registry
3. Verify overlaybd cache settings

## Migration from Containerd

If migrating existing setups from containerd to Docker:

1. Images remain compatible (same overlaybd format)
2. Existing pulled images can be reused
3. Configuration changes required:
   - Add `runtimeType: "docker"` to snapshotter config
   - Enable containerd snapshotter in Docker daemon
4. Restart services in order:
   - overlaybd-tcmu
   - overlaybd-snapshotter
   - containerd
   - Docker

## References

- [QUICKSTART](QUICKSTART.md) - General overlaybd setup
- [EXAMPLES](EXAMPLES.md) - Running overlaybd containers
- [TurboOCI](TURBO_OCI.md) - On-the-fly OCI image acceleration
- [Configuration](../script/config.json) - Full configuration options
