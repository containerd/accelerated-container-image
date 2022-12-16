# Standalone Userspace Image Convertor

We provide a tool to convert OCIv1 images into overlaybd format in userspace, without the dependences of containerd and tcmu. Only several ovelraybd tools binary are required.

This convertor is stored in `bin` after `make`.

This is an experimental feature and will be continuously improved.


# Requirement

There's no need to install containerd, no need to launch and mount tcmu devices, no need to run as root.
Only several tools are required:

- overlaybd-create, overlaybd-commit and overlaybd-apply

  Three overlaybd tools provided in [overlaybd](https://github.com/containerd/overlaybd), stored at `/opt/overlaybd/bin`.

- baselayer

  stored at `/opt/overlaybd/baselayers/ext4_64` after installing [overlaybd](https://github.com/containerd/overlaybd). This is only required just for now. Once the automatic mkfs is implemented, it's no longer needed.

Overall, the requirements are `/opt/overlaybd/bin/{overlaybd-create,overlaybd-commit,overlaybd-apply}` and `/opt/overlaybd/baselayers/ext4_64`.

# Basic Usage

```bash
# usage
$ bin/convertor --help

overlaybd-convertor is a standalone userspace image conversion tool that helps converting oci images to overlaybd images

Usage:
  overlaybd-convertor [flags]

Flags:
  -r, --repository string   repository for converting image (required)
  -u, --username string     user[:password] Registry user and password
      --plain               connections using plain HTTP
  -i, --input-tag string    tag for image converting from (required)
  -o, --output-tag string   tag for image converting to (required)
  -d, --dir string          directory used for temporary data (default "tmp_conv")
  -h, --help                help for overlaybd-convertor

# example
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 -o 6.2.6_obd

```

# Make Conversion Faster

We have provided a [customized libext2fs](https://github.com/data-accelerator/e2fsprogs), which is easy to use and can significantly improve the efficiency of this standalone userspace image-convertor. See [USERSPACE_IMAGE_CONVERTOR](https://github.com/containerd/overlaybd/blob/main/docs/USERSPACE_IMAGE_CONVERTOR.md) for more details.
