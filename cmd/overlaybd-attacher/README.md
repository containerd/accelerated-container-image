# overlaybd-attacher

A command-line tool to attach and detach overlaybd block devices through the kernel's configfs interface.

## Overview

`overlaybd-attacher` provides a simple interface to manage overlaybd devices by calling `AttachDevice` and `DetachDevice` functions from `pkg/snapshot/storage.go`. It creates TCMU (TCM User) devices via the Linux kernel's configfs target subsystem.

## Installation

Build from source:

```bash
make bin/overlaybd-attacher
```

The binary will be generated in the `bin/` directory.

## Usage

### Attach Device

Attach an overlaybd device with a given configuration:

```bash
overlaybd-attacher attach --id <device-id> --config <config-file> [options]
```

**Options:**

- `--id` (required): Device ID (snapshot ID)
- `--config` (required): Path to overlaybd configuration file (config.v1.json)
- `-v, --verbose`: Enable verbose logging

**Note:** Specifying custom tenant ID is not supported. The tool uses the default tenant (-1).

**Example:**

```bash
# Basic attach
overlaybd-attacher attach --id 123456 --config /path/to/config.v1.json

# With verbose logging
overlaybd-attacher attach --id 123456 --config /path/to/config.v1.json -v
```

The command outputs the device path (e.g., `/dev/sda`) to stdout on success.

**Debug Log:**

The attach process writes debug information to a result file. The location is determined by:

1. If `resultFile` is specified in config.v1.json:
   - Use the specified path (absolute or relative to config file directory)
2. If `resultFile` is not specified:
   - Default to `init-debug.log` in the same directory as the config file

This file contains detailed information about the device attachment process and can be used for troubleshooting.

**Example config.v1.json:**

```json
{
  "repoBlobUrl": "...",
  "lowers": [...],
  "upper": {...},
  "resultFile": "/var/log/overlaybd/init-debug.log"
}
```

or with relative path:

```json
{
  "repoBlobUrl": "...",
  "lowers": [...],
  "upper": {...},
  "resultFile": "debug.log"
}
```

### Detach Device

Detach an overlaybd device:

```bash
overlaybd-attacher detach --id <device-id> [options]
```

**Options:**

- `--id` (required): Device ID (snapshot ID)
- `-v, --verbose`: Enable verbose logging

**Note:** Specifying custom tenant ID is not supported. The tool uses the default tenant (-1).

**Example:**

```bash
# Basic detach
overlaybd-attacher detach --id 123456

# With verbose logging
overlaybd-attacher detach --id 123456 -v
```

## Retry Mechanism

The `attach` command includes an automatic retry mechanism on failure:

- Retry count: 5 attempts (hardcoded)
- Retry interval: 1 second between attempts
- Retries occur automatically without user intervention

This helps handle transient failures during device attachment, such as temporary resource unavailability or timing issues with kernel module operations.

## Implementation Details

### Device Creation Process

The `attach` command performs the following operations:

1. Creates TCMU device in configfs: `/sys/kernel/config/target/core/user_<hba>/<dev_id>`
2. Configures device parameters (max_data_area_mb, cmd_time_out)
3. Creates loopback target: `/sys/kernel/config/target/loopback/naa.<prefix><id>`
4. Creates loopback LUN and links to TCMU device
5. Waits for SCSI block device to appear in `/sys/class/scsi_device/`
6. Returns device path (e.g., `/dev/sda`)

### Device Destruction Process

The `detach` command performs cleanup in reverse order:

1. Removes loopback LUN link
2. Removes loopback target configuration
3. Removes TCMU device from configfs

## Multi-tenancy Support

**Note:** Specifying custom tenant ID is not supported. The tool always uses the default tenant (-1), which corresponds to HBA 999999999 and NAA prefix 199.

## Requirements

- Linux kernel with TCM_LOOPBACK and TCM_USER modules loaded
- Overlaybd backstore process running
- Root privileges (required for configfs operations)

## Exit Codes

- `0`: Success
- `1`: Error occurred (check stderr for details)

## Logging

Logs are written to stderr using logrus. Enable verbose logging with `-v` flag for detailed operation information.