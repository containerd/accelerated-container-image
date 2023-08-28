# Performance

This document aims to compare the startup speed between Accelerated Container Image and standard OCI tar image.

## Environment

Two WordPress images: one is in overlaybd format, the other is in standard OCI tar format.

## Why WordPress?

WordPress is one of the most popular solutions to build a website and setup a web service. Its main process is Apache server.

Unlike other lightweight apps such as busybox, WordPress image contains a lot of static resource files that need to be extracted before the admin logs in for the first time. Some IO and CPU workloads will thus be created. So we think it an appropriate way to simulate user actions in real business scenarios.

## Steps

We provide a setup script to measure the service available time of WordPress app. Before start, please make sure port 80 is not occupied in your host.

Use the clean script to stop running containers, remove images and drop local cache.

```bash
cd script/performance/

./clean-env.sh
time ./setup-wordpress.sh registry.hub.docker.com/overlaybd/wordpress:5.7.0_obd

./clean-env.sh
time ./setup-wordpress.sh registry.hub.docker.com/overlaybd/wordpress:5.7.0
```

## Performance results

| **Image Format** | **Service Available Time** |
| :----: | :----: |
| overlaybd | 6.433s |
| OCI tar | 15.341s |

Conclusion: Overlaybd format outperforms OCI tar format at startup speed.

Note: The [prefetch](https://github.com/containerd/accelerated-container-image/blob/main/docs/trace-prefetch.md) feature is enabled.

Note: [Here](./TURBO_OCI.md) is the TurboOCI images' performance compares with OverlayBD and OCI images.
