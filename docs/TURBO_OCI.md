# Overlaybd - TurboOCIv1

__Overlaybd - TurboOCIv1__ (_TurboOCI_ for short) image, which means you can use OCIv1 images fastly. If you convert OCI image to overlaybd image to use remotely, you need to provide additional time to convert and space (may more than double) to store and manage the overlaybd image. But in TurboOCI, only you need is build a meta image for OCI image which contains only a small amount of metadata.

## Feature

### No image conversion

This is problematic for container developers who don't want to manage the cost and complexity of keeping copies of images in two formats. It also creates problems for image signing, since the conversion step invalidates any signatures that were created against the original OCI image.

TurboOCI addresses these issues by loading from the original, unmodified OCI image. Instead of converting the image, it builds a separate image with a small amount of data of the ext4 filesystem (which is the "meta image"), which lives in the remote registry and usually has 3% size of the original image.

### Good stability, reliability and performance

At present, the products similar to TurboOCI on the market are mainly AWS's FUSE-based solution SOCI. As a user mode file system, FUSE will frequently switch between user mode and kernel mode, and cannot be recovered after restart. TurboOCI uses block devices the same as overlaybd whose performance and stability performance is far superior to FUSE. Relying on Alibaba Cloud's DADI acceleration link, you can obtain performance close to __Overlaybd Native__ images with less space.

### Compatible with overlaybd

TurboOCI is compatible with overlaybd format. Even for different layers of the same image, you can use overlaybd and TurboOCI at the same time without conflicts. You can use TurboOCI in much the same way as you used overlaybd.

## Usage

### Configure

The format of OCI image's data may be tar or gzip (depending on `mediatype` of the OCI image layer). For the gzip format, we provide a cache of the decompressed data, you can modify the gzip cache options in `/etc/overlaybd/config.json`.

```json
{
    // ...
    "gzipCacheConfig": {
        "enable": true,
        "cacheSizeGB": 4,
        "cacheDir": "/opt/overlaybd/gzip_cache"
    },
    // ...
}
```

After this setting, when reading the gzip data of the OCI image, the data will be decompressed into `/opt/overlaybd/gzip_cache`, and the size of the cache pool is 4GB.

### Build

Before building the TurboOCI image, you should make sure you have permission to pull and push images to the repo.

```bash
# build Overlaybd-turboOCIv1 image
bin/convertor -r <repo> -i <input-tag> --turboOCI <turboOCI-tag>

# build Overlaybd-native image
bin/convertor -r <repo> -i <input-tag> --overlaybd <overlaybd-tag>

# build both turboOCI and overlaybd in one task
bin/convertor -r <repo> -i <input-tag> --turboOCI <turboOCI-tag> --overlaybd <overlaybd-tag>
```

### Pull and run

It is the same with the overlaybd image, see [QUICKSTART](./QUICKSTART).

## Performance

We used a series of images with different characteristics to test the performance of turboOCI, overlaybd, and OCI images.

"Image Size" of the turboOCI image refers to the size of the meta image in the registry. The value in brackets is the decompressed size of the gzip data of the OCI image.

"Service Available Time" is the time from image pull to service available. For OCI images, this includes the time to download all data, for turboOCI and overlaybd, "pull" means executing `rpull`.


**ai-cat-or-dog**

An AI model consumes a lot of resources.

| **Image Format** | **Image Size** | **Service Available Time** |
| :----: | :----: | :----: |
| Overlaybd - TurboOCIv1 | 29.1 MB | 14.65s |
| Overlaybd - Native| 1.09 GB | 11.72s |
| OCIv1 gzip | 730.2 MB (1.81 GB) | 30.77s |


**php-laravel-nginx**

An nginx server similar to the user's actual behavior.

| **Image Format** | **Image Size** | **Service Available Time** |
| :----: | :----: | :----: |
| Overlaybd - TurboOCIv1 | 11.35 MB | 4.15s |
| Overlaybd - Native | 326.3 MB | 3.74s |
| OCI gzip | 193.3 MB (567 MB) | 9.47s |


**python-small-import**

A Flask app with a small number of import libraries.

| **Image Format** | **Image Size** | **Service Available Time** |
| :----: | :----: | :----: |
| Overlaybd - TurboOCIv1 | 30.3 MB | 4.61s |
| Overlaybd - Native | 1.31 GB | 3.6s |
| OCI gzip | 728.1 MB (2.51 GB) | 30.38s |

**python-large-import**

A Flask app with a large number of import libraries.

| **Image Format** | **Image Size** | **Service Available Time** |
| :----: | :----: | :----: |
| Overlaybd - TurboOCIv1 | 30.3 MB | 9.09s |
| Overlaybd - Native | 1.31 GB | 7.77s |
| OCI gzip | 728.1 MB (2.51 GB) | 31.2s |
