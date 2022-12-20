# Standalone Userspace Image Convertor

Standalone userspace image-convertor is a tool to convert OCIv1 images into overlaybd format in userspace, without the dependences of containerd and tcmu. Only several ovelraybd tools binary are required.

This convertor is stored in `bin` after `make`.

This is an experimental feature and will be continuously improved.


## Requirement

There's no need to install containerd, no need to launch and mount tcmu devices, no need to run as root.
Only several tools are required:

- overlaybd-create, overlaybd-commit and overlaybd-apply

  Three overlaybd tools provided in [overlaybd](https://github.com/containerd/overlaybd), stored at `/opt/overlaybd/bin`.

- baselayer

  stored at `/opt/overlaybd/baselayers/ext4_64` after installing [overlaybd](https://github.com/containerd/overlaybd). This is only required just for now. Once the automatic mkfs is implemented, it's no longer needed.

Overall, the requirements are `/opt/overlaybd/bin/{overlaybd-create,overlaybd-commit,overlaybd-apply}` and `/opt/overlaybd/baselayers/ext4_64`.

## Basic Usage

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

## libext2fs

Standalone userspace image-convertor is developed based on [libext2fs](https://github.com/tytso/e2fsprogs), and we have provided a [customized libext2fs](https://github.com/data-accelerator/e2fsprogs) to make the conversion faster. We used `standalone userspace image-convertor (with custom libext2fs)`, `standalone userspace image-convertor (with origin libext2fs)` and `embedded image-convertor` to convert some images and did a comparison for reference.

### Performance

| Image               | Image Size | with custom libext2fs | with origin libext2fs | embedded image-convertor |
|:-------------------:|:----------:|:---------------------:|:---------------------:|:------------------------:|
| jupyter-notebook    | 4.84 GB    | 93 s                  | 238 s                 | 101 s                    |
| php-laravel-nginx   | 567 MB     | 13 s                  | 20 s                  | 15 s                     |
| ai-cat-or-dog       | 1.81 GB    | 27 s                  | 54 s                  | 60 s                     |
| cypress-chrome      | 2.73 GB    | 70 s                  | 212 s                 | 87 s                     |

### Use with origin libext2fs

Standalone userspace image-convertor uses customized libext2fs by default. If you want to use original libext2fs instead of it, see the cmake cache entry `-D ORIGIN_EXT2FS=1` of [overlaybd](https://github.com/containerd/overlaybd#build).
