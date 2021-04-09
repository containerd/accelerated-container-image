# Performance

This document aims to compare the start-up speed between Accelerated Container Image and standard OCI tarball image.

## Environment

Two WordPress images, served on Aliyun ACR (image registry service).

One is in overlaybd format, the other is in standard OCI format.

## Why WordPress?

WordPress is one of the most popular solutions to build a website and setup a web service. Its main process is Apache server.

Unlike other lightweight apps such as busybox, WordPress image contains a lot of static resource files that need to be extracted before the admin logs in for the first time. Some IO and CPU workloads will thus be created. So we think it an appropriate way to simulate user actions in real business scenarios.

## Steps

We provide a setup script to measure the service available time of WordPress app. Before start, please make sure port 80 is not occupied in your host.

Use the clean script to stop running containers, remove images and drop local cache.

```bash
cd script/performance/

./clean-env.sh
time ./setup-wordpress.sh overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/wordpress:5.6.2_obd

./clean-env.sh
time ./setup-wordpress.sh overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/wordpress:5.6.2
```

## Performance results

| **Image Format** | **Service Available Time** |
| :----: | :----: |
| overlaybd | 6.433s |
| standard tarball | 15.341s |

Note: The prefetch feature is enabled.

Conclusion: Overlaybd outperforms standard OCI tarball in terms of cold start time (without local cache).
