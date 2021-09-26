# Multi-FS Supporting in Overlaybd 

## Base Layer
Base layer is the lowermost layer of overlaybd layers, which is usually a block device formatted by specified file system and can be shared by different images.

We use ext4 file system by default.

## Build Base Layer with Specified File System
It's supported to build a new base layer with specified file system and reuse it in converting images.

### Build Base Layer Only
Building base layer is implemented in overlaybd image convertor. 

Use `--build-baselayer-only` option to run build base layer only mode, use `--basepath` option to specify base layer path, and use `--fstype` to specify file system.

Take `xfs` file system as an example:

```bash
sudo bin/ctr obdconv --build-baselayer-only --basepath /your/path/xfs_baselayer --fstype "xfs"
```

### Mount/Mkfs options
It's supported to use different options to mount/mkfs for specified file system.

Specify mount/mkfs options in `--fstype` option, for example, `--fystype "fstype;mount_opt_1,mount_opt_2;mkfs_opt_1 mkfs_opt_2"`.
If only file system type is given, we use default mount/mkfs option to fulfill better performance.

+ Default Mount/Mkfs Options

|FS Type|Mount Options|Mkfs Options|
|---|---|---|
|ext4|discard|-O ^has_journal,sparse_super,flex_bg -G 1 -E discard|
|xfs|nouuid,discard|-f -l size=4m -m crc=0|
|ntfs|-|-F -f|

+ Override Default Options

It's valid to override default mount/mkfs options. 

|`--fstype`|Description|
|---|---|
|`"fstype"`|use default mount/mkfs options|
|`"fstype;"`|invalid mount options, and use default mkfs options|
|`"fstype;;"`|invalid mount/mkfs options|
|`"fstype;;mkfs_opts"`|invalid mount options, and use specified mkfs options|
|`"fstype;mount_opts"`|use specified mount options, and use default mkfs options|
|`"fstype;mount_opts;"`|use specified mount options, and invalid mkfs options|
|`"fstype;mount_opts;mkfs_options"`|use specified mount/mkfs options|

## Convert OCI Image into overlaybd with Specified File System
You can specify file system and base layer in converting OCI Image into overlaybd with `--fstype` and `--basepath` options.

For example:

```bash
sudo bin/ctr obdconv --basepath /your/path/xfs_baselayer --fstype "xfs" registry.hub.docker.com/library/redis:6.2.1 localhost:5000/redis:6.2.1_obd_xfs
```

If `basepath` exists, overlaybd image convertor will use it as base layer, otherwise convertor will build a new base layer with `fstype` option and store it to `basepath`.

Push the generated overlaybd image to registry, then pull the uploaded image with `rpull` as described in [EXAMPLES](EXAMPLES.md).