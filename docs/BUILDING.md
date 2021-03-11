# Build from source

This doc includes:

* [Build requirements](#build-requirements)
* [Build the development environment](#build-the-development-environment)
  * [Requirements](#requirements)
  * [Build](#build)
* [Configure](#configure)
  * [About proxy snapshotter plugin for containerd](#about-proxy-snapshotter-plugin-for-containerd)

## Build Requirements

Go >= 1.15.x is required.

## Build the development environment

### Requirements

* OverlayBD proxy snapshotter required containerd daemon >= 1.4.x
  * Require [supports target snapshot references on prepare](https://github.com/containerd/containerd/pull/3793).
  * See [Downloads at containerd.io](https://containerd.io/downloads/).

### Build

You need git to checkout the source code and compile:

```bash
git clone https://github.com/alibaba/accelerated-container-image.git
cd accelerated-container-image
make
```
The snapshotter and ctr plugin are generated in `bin`.

## Configure

### About proxy snapshotter plugin for containerd

```bash
sudo cat <<-EOF | sudo tee --append /etc/containerd/config.toml

[proxy_plugins.overlaybd]
   type = "snapshot"
   address = "/run/overlaybd-snapshotter/overlaybd.sock"
EOF

# don't forget to restart the containerd daemon.
sudo systemctl restart containerd
```
