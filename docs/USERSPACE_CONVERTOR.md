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

  stored at `/opt/overlaybd/baselayers/ext4_64` after installing [overlaybd](https://github.com/containerd/overlaybd). This is required if flag `--mkfs` is false.

Overall, the requirements are `/opt/overlaybd/bin/{overlaybd-create,overlaybd-commit,overlaybd-apply}` and `/opt/overlaybd/baselayers/ext4_64`(optional).

## Basic Usage

```bash
# usage
$ bin/convertor --help

overlaybd convertor is a standalone userspace image conversion tool that helps converting oci images to overlaybd images

Usage:
  convertor [flags]

Flags:
  -r, --repository string         repository for converting image (required)
  -u, --username string           user[:password] Registry user and password
      --plain                     connections using plain HTTP
      --verbose                   show debug log
  -i, --input-tag string          tag for image converting from (required)
  -o, --output-tag string         tag for image converting to
  -d, --dir string                directory used for temporary data (default "tmp_conv")
      --oci                       export image with oci spec
      --mkfs                      make ext4 fs in bottom layer (default true)
      --vsize int                 virtual block device size (GB) (default 64)
      --fastoci string            build 'Overlaybd-Turbo OCIv1' format (old name of turboOCIv1. deprecated)
      --turboOCI string           build 'Overlaybd-Turbo OCIv1' format
      --overlaybd string          build overlaybd format
      --db-str string             db str for overlaybd conversion
      --db-type string            type of db to use for conversion deduplication. Available: mysql. Default none
      --concurrency-limit int     the number of manifests that can be built at the same time, used for multi-arch images, 0 means no limit (default 4)
      --cert-dir stringArray      In these directories, root CA should be named as *.crt and client cert should be named as *.cert, *.key
      --root-ca stringArray       root CA certificates
      --client-cert stringArray   client cert certificates, should form in ${cert-file}:${key-file}
      --insecure                  don't verify the server's certificate chain and host name
      --reserve                   reserve tmp data
      --no-upload                 don't upload layer and manifest
      --dump-manifest             dump manifest
  -h, --help                      help for convertor

# examples
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 -o 6.2.6_obd
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 --overlaybd 6.2.6_obd --fastoci 6.2.6_foci
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 -o 6.2.6_obd --vsize 256

```

### Layer/Manifest Deduplication

To avoid converting the same layer for every image conversion, a database is required to store the correspondence between OCIv1 image layer and overlaybd layer.

We provide a default implementation based on mysql database, but others can be added through the ConversionDatabase abstraction. To use the default:

First, create a database and the `overlaybd_layers` table, the table schema is as follows:

```sql
CREATE TABLE `overlaybd_layers` (
  `host` varchar(255) NOT NULL,
  `repo` varchar(255) NOT NULL,
  `chain_id` varchar(255) NOT NULL COMMENT 'chain-id of the normal image layer',
  `data_digest` varchar(255) NOT NULL COMMENT 'digest of overlaybd layer',
  `data_size` bigint(20) NOT NULL COMMENT 'size of overlaybd layer',
  PRIMARY KEY (`host`,`repo`,`chain_id`),
  KEY `index_registry_chainId` (`host`,`chain_id`) USING BTREE
) DEFAULT CHARSET=utf8;
```

If you also want caching for manifests to avoid reconverting the same manifest twice, you can create the `overlaybd_manifests` table, the table schema is as follows:

```sql
CREATE TABLE `overlaybd_manifests` (
  `host` varchar(255) NOT NULL,
  `repo` varchar(255) NOT NULL,
  `src_digest` varchar(255) NOT NULL COMMENT 'digest of the normal image manifest',
  `out_digest` varchar(255) NOT NULL COMMENT 'digest of overlaybd manifest',
  `data_size` bigint(20) NOT NULL COMMENT 'size of overlaybd manifest',
  `mediatype` varchar(255) NOT NULL COMMENT 'mediatype of the converted image manifest',
  PRIMARY KEY (`host`,`repo`,`src_digest`, `mediatype`),
  KEY `index_registry_src_digest` (`host`,`src_digest`) USING BTREE
) DEFAULT CHARSET=utf8;
```

with this database you can then provide the following flags:

```bash
Flags:
    --db-str          db str for overlaybd conversion
    --db-type         type of db to use for conversion deduplication. Available: mysql. Default none

# example
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 -o 6.2.6_obd --db-str "dbuser:dbpass@tcp(127.0.0.1:3306)/dedup" --db-type mysql
```

* Note that we have also provided some tools to create such a database and examples of usage as well as a dockerfile that could be used to setup a simple converter with caching capabilities, see [samples](../cmd/convertor/resources/samples).

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
