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
    },
    "storage-driver": "overlaybd"
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
# Run with overlaybd snapshotter
docker run --rm -it registry.hub.docker.com/overlaybd/redis:7.2.3_obd

Unable to find image 'registry.hub.docker.com/overlaybd/redis:7.2.3_obd' locally
7.2.3_obd: Pulling from overlaybd/redis
Digest: sha256:d4c391ded1cdd1752f0ec14a5c594c8aa072983af10f29032294f4588dc6a5fc
Status: Downloaded newer image for registry.hub.docker.com/overlaybd/redis:7.2.3_obd
1:C 26 Apr 2026 05:09:54.784 # WARNING Memory overcommit must be enabled! Without it, a background save or replication may fail under low memory condition. Being disabled, it can also cause failures without low memory condition, see https://github.com/jemalloc/jemalloc/issues/1328. To fix this issue add 'vm.overcommit_memory = 1' to /etc/sysctl.conf and then reboot or run the command 'sysctl vm.overcommit_memory=1' for this to take effect.
1:C 26 Apr 2026 05:09:54.784 * oO0OoO0OoO0Oo Redis is starting oO0OoO0OoO0Oo
1:C 26 Apr 2026 05:09:54.784 * Redis version=7.2.3, bits=64, commit=00000000, modified=0, pid=1, just started
1:C 26 Apr 2026 05:09:54.784 # Warning: no config file specified, using the default config. In order to specify a config file use redis-server /path/to/redis.conf
1:M 26 Apr 2026 05:09:54.785 * monotonic clock: POSIX clock_gettime
                _._
           _.-``__ ''-._
      _.-``    `.  `_.  ''-._           Redis 7.2.3 (00000000/0) 64 bit
  .-`` .-```.  ```\/    _.,_ ''-._
 (    '      ,       .-`  | `,    )     Running in standalone mode
 |`-._`-...-` __...-.``-._|'` _.-'|     Port: 6379
 |    `-._   `._    /     _.-'    |     PID: 1
  `-._    `-._  `-./  _.-'    _.-'
 |`-._`-._    `-.__.-'    _.-'_.-'|
 |    `-._`-._        _.-'_.-'    |           https://redis.io
  `-._    `-._`-.__.-'_.-'    _.-'
 |`-._`-._    `-.__.-'    _.-'_.-'|
 |    `-._`-._        _.-'_.-'    |
  `-._    `-._`-.__.-'_.-'    _.-'
      `-._    `-.__.-'    _.-'
          `-._        _.-'
              `-.__.-'
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
