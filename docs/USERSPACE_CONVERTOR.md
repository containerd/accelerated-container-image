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
  -i, --input-tag string          tag for image converting from (required when input-digest is not set)
  -g, --input-digest string       digest for image converting from (required when input-tag is not set)
  -o, --output-tag string         tag for image converting to
  -d, --dir string                directory used for temporary data (default "tmp_conv")
      --oci                       export image with oci spec
      --fstype string             filesystem type of converted image. (default "ext4")
      --mkfs                      make ext4 fs in bottom layer (default true)
      --vsize int                 virtual block device size (GB) (default 64)
      --fastoci string            build 'Overlaybd-Turbo OCIv1' format (old name of turboOCIv1. deprecated)
      --turboOCI string           build 'Overlaybd-Turbo OCIv1' format
      --overlaybd string          build overlaybd format
      --db-str string             db str for overlaybd conversion
      --db-type string            type of db to use for conversion deduplication. Available: mysql. Default none
      --concurrency-limit int     the number of manifests that can be built at the same time, used for multi-arch images, 0 means no limit (default 4)
      --disable-sparse            disable sparse file for overlaybd
      --referrer                  push converted manifests with subject, note '--oci' will be enabled automatically if '--referrer' is set, cause the referrer must be in OCI format.
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
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -g sha256:309f99718ff2424f4ae5ebf0e46f7f0ce03058bf47d9061d1d66e4af53b70ffc -o 309f99718ff2424f4ae5ebf0e46f7f0ce03058bf47d9061d1d66e4af53b70ffc_obd
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 --overlaybd 6.2.6_obd --fastoci 6.2.6_foci
$ bin/convertor -r docker.io/overlaybd/redis -u user:pass -i 6.2.6 -o 6.2.6_obd --vsize 256

```

### Referrers API support (Experimental)

Referrers API provides the ability to reference artifacts to existing artifacts, it returns all artifacts that have a `subject` field of the given manifest digest. If your registry has supported this feature, you can enable `--referrer` so that the converted image will be referenced to the original image. See [Listing Referrers](https://github.com/opencontainers/distribution-spec/blob/v1.1.0/spec.md#listing-referrers) and  for more details.

The artifact type for overlaybd and turboOCIv1 is `application/vnd.containerd.overlaybd.native.v1+json` and `application/vnd.containerd.overlaybd.turbo.v1+json` respectively.

The format of the converted images is as follows, note that if the original image is an index (multi-arch image), all converted indexes and manifests will have a `subject` field.

#### index.json

```
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "artifactType": "application/vnd.containerd.overlaybd.native.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:b0a40a33547de0961b6e0064a298e55484a2636830ba8bf5d05e34fae88b1443",
      "size": 882,
      "platform": {
        "architecture": "386",
        "os": "linux"
      }
    },
    ...
  ],
  "subject": {
    "mediaType": "application/vnd.docker.distribution.manifest.list.v2+json",
    "digest": "sha256:5df8d0e068b9c8c95c330607dfd96db51ac0b670b3a974ab23449866c0aa70a1",
    "size": 1076
  }
}
```

#### manifest.json

```
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.containerd.overlaybd.native.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:e006fc50e6cec5e81844abb28abd0a01f4ff599432818a3bb9dfb96ce3e5daae",
    "size": 571
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar",
      "digest": "sha256:63e766ab33f12958a0d94676a0bf5f7b800e04e4fac2124d785a8a54a8108e45",
      "size": 3933696,
      "annotations": {
        "containerd.io/snapshot/overlaybd/blob-digest": "sha256:63e766ab33f12958a0d94676a0bf5f7b800e04e4fac2124d785a8a54a8108e45",
        "containerd.io/snapshot/overlaybd/blob-size": "3933696",
        "containerd.io/snapshot/overlaybd/version": "0.1.0"
      }
    }
  ],
  "subject": {
    "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
    "digest": "sha256:096958b089cdfa4b345dba0ae0a1e43bea59e4de6e084c26429b6c85096322cf",
    "size": 528
  }
}
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
