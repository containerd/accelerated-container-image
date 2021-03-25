# Performance

This document aims to compare the start-up speed between Accelerated Container Image and standard OCI tarball image.

## Environment

Two WordPress images, served on Aliyun ACR (image registry service).

One is in overlaybd format, the other is in standard OCI format.

## Why WordPress?

WordPress is one of the most popular solutions to build a website and setup a web service. Its main process is Apache server.

Unlike other lightweight apps such as busybox, WordPress image contains a lot of static resource files that need to be extracted before the admin logs in for the first time. Some IO and CPU workloads will thus be created. So we think it an appropriate way to simulate user actions in real business scenarios.

## Steps

We provide a setup script to measure the Process Available Time and Service Available Time of WordPress app. Before start, please make sure port 80 is not occupied in your host.

Use the clean script to stop running containers and remove images.

```bash
cd script/performance/

time ./setup-wordpress.sh overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/wordpress:5.6.2_obd
./clean-wordpress.sh overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/wordpress:5.6.2_obd

time ./setup-wordpress.sh overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/wordpress:5.6.2
./clean-wordpress.sh overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/wordpress:5.6.2
```

## Performance results

| Image Format | Process Available Time | Service Available Time |
| :----: | :----: | :----: |
| **overlaybd** | 2.606s | 4.080s |
| **standard tarball** | 12.752s | 17.203s |

Conclusion: Accelerated Container Image's overlaybd format outperforms standard OCI tarball image in terms of container start-up speed.

## Further actions

We will introduce a new performance improvement based on prefetch technology in the next major release. The time cost will continue to decrease to approximately 50%.
