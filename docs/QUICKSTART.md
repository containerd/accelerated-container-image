# Quickstart Guide

This guide helps to config and run the common case of overlaybd image service.

- [Quickstart Guide](#quickstart-guide)
  - [Install](#install)
    - [overlaybd-snapshotter](#overlaybd-snapshotter)
      - [Compile from source](#compile-from-source)
      - [Download release](#download-release)
      - [Config](#config)
      - [Start service](#start-service)
    - [overlaybd-tcmu](#overlaybd-tcmu)
      - [Compile from source](#compile-from-source-1)
      - [Download release](#download-release-1)
      - [Config](#config-1)
      - [Start service](#start-service-1)
  - [Configuration](#configuration)
    - [Containerd](#containerd)
    - [Authentication](#authentication)
  - [Run overlaybd images](#run-overlaybd-images)
  - [Image conversion](#image-conversion)
  - [Image build](#image-build)
    - [Install](#install-1)
    - [Run buildkitd](#run-buildkitd)
  - [P2P](#p2p)

## Install

There are two components to be installed, overlaybd-snapshotter and overlaybd-tcmu. They are located in two separate repositries.
- overlaybd-snapshotter is in this repositry
- overlaybd-tcmu is in https://github.com/containerd/overlaybd.

### overlaybd-snapshotter

Users can compile the latest code to install or download the [release](https://github.com/containerd/accelerated-container-image/releases).

#### Compile from source

Install dependencies:
- golang 1.22+

Run the following commands to build:
```bash
git clone https://github.com/containerd/accelerated-container-image.git
cd accelerated-container-image
make
sudo make install
```

#### Download release

After download, install the rpm/deb package.

#### Config

The config file is `/etc/overlaybd-snapshotter/config.json`. Please create the file if not exists.
**We suggest the root path of snapshotter is a subpath of containerd's root**

```json
{
    "root": "/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd",
    "address": "/run/overlaybd-snapshotter/overlaybd.sock",
    "verbose": "info",
    "rwMode": "overlayfs",
    "logReportCaller": false,
    "autoRemoveDev": false,
    "exporterConfig": {
        "enable": false,
        "uriPrefix": "/metrics",
        "port": 9863
    },
    "mirrorRegistry": [
        {
            "host": "localhost:5000",
            "insecure": true
        },
        {
            "host": "registry-1.docker.io",
            "insecure": false
        }
    ]
}
```
| Field | Description |
| ----- | ----------- |
| `root` | the root directory to store snapshots. **Suggestion: This path should be a subpath of containerd's root** |
| `address` | the socket address used to connect withcontainerd. |
| `verbose` | log level, `info` or `debug` |
| `rwMode` | rootfs mode about wether to use native writable layer. See [Native Support for Writable](./WRITABLE.md) for detail. |
| `logReportCaller` | enable/disable the calling method |
| `autoRemoveDev` | enable/disable auto clean-up overlaybd device after container removed |
| `exporterConfig.enable` | whether or not create a server to show Prometheus metrics |
| `exporterConfig.uriPrefix` | URI prefix for export metrics, default `/metrics` |
| `exporterConfig.port` | port for http server to show metrics, default `9863` |
| `mirrorRegistry` | an arrary of mirror registries |
| `mirrorRegistry.host` | host address, eg. `registry-1.docker.io`` |
| `mirrorRegistry.insecure` | `true` or `false` |


#### Start service

Run the `/opt/overlaybd/snapshotter/overlaybd-snapshotter` binary or start it as a service by enable and start [overlaybd-snapshotter.service](https://github.com/containerd/accelerated-container-image/blob/main/script/overlaybd-snapshotter.service).

If installed from source, please run the following to start service.
```bash
sudo systemctl enable /opt/overlaybd/snapshotter/overlaybd-snapshotter.service
sudo systemctl start overlaybd-snapshotter
```

### overlaybd-tcmu

Users can compile the latest code to install or download the [release](https://github.com/containerd/overlaybd/releases).

There is no strong dependency between the overlaybd-snapshotter and overlaybd-tcmu versions. However, overlaybd-snapshotter v1.0.1+ requires overlaybd-tcmu v1.0.4+ because there have been adjustments made to the parameters for image conversion.

#### Compile from source

Install dependencies:
- cmake 3.15+
- gcc/g++ 7+
- development dependencies:
    - CentOS 7/Fedora: `sudo yum install libaio-devel libcurl-devel openssl-devel libnl3-devel libzstd-static e2fsprogs-devel`
    - CentOS 8: `sudo yum install libaio-devel libcurl-devel openssl-devel libnl3-devel libzstd-devel e2fsprogs-devel`
    - Debian/Ubuntu: `sudo apt install libcurl4-openssl-dev libssl-dev libaio-dev libnl-3-dev libnl-genl-3-dev libgflags-dev libzstd-dev libext2fs-dev`

Run the following commands to build:
```bash
git clone https://github.com/containerd/overlaybd.git
cd overlaybd
git submodule update --init
mkdir build
cd build
cmake -DCMAKE_BUILD_TYPE=Release ..
make -j
sudo make install
```

#### Download release

After download, install the rpm/deb package.

#### Config

The config file is `/etc/overlaybd/overlaybd.json`. The default if installed automatically and can be used directly without change.

For more details please refer to [configuration](https://github.com/containerd/overlaybd/blob/main/README.md#configuration).

#### Start service

```bash
sudo systemctl enable /opt/overlaybd/overlaybd-tcmu.service
sudo systemctl start overlaybd-tcmu
```

## Configuration

### Containerd

Containerd 1.4+ is required.

Add snapshotter config to containerd config file (default `/etc/containerd/config.toml`).

```toml
[proxy_plugins.overlaybd]
    type = "snapshot"
    address = "/run/overlaybd-snapshotter/overlaybd.sock"
```

If k8s/cri is used, add the following config.

```toml
[plugins.cri]
    [plugins.cri.containerd]
        snapshotter = "overlaybd"
        disable_snapshot_annotations = false
```

Make sure `cri` is not listed in `disabled_plugins` in containerd config file.

At last do not forget to restart containerd.

### Authentication

Since Authentication cannot share between containerd and overlaybd-tcmu, authentication has to be configured to overlaybd-tcmu individually.

The auth config file path for ovelaybd-tcmu can be specified in `/etc/overlaybd/overlaybd.json`. (default `/opt/overlaybd/cred.json`).

This is an example, and the format is the same as docker auth file (`/root/.docker/config.json`)
```json
{
  "auths": {
    "hub.docker.com": {
      "username": "username",
      "password": "password"
    },
    "hub.docker.com/hello/world": {
      "auth": "dXNlcm5hbWU6cGFzc3dvcmQK"
    }
  }
}
```

## Run overlaybd images

Now users can run overlaybd images.
There are several methods.

- use nerdctl

    ```bash
    sudo nerdctl run --net host -it --rm --snapshotter=overlaybd registry.hub.docker.com/overlaybd/redis:6.2.1_obd
    ```

- use rpull

    ```bash
    # use rpull to pull image without layer downloading
    sudo /opt/overlaybd/snapshotter/ctr rpull -u {user}:{pass} registry.hub.docker.com/overlaybd/redis:6.2.1_obd

    # run by ctr run
    sudo ctr run --net-host --snapshotter=overlaybd --rm -t registry.hub.docker.com/overlaybd/redis:6.2.1_obd demo
    ```

- use k8s/cri

    Run with k8s or crictl, refer to [EXAMPLES_CRI](https://github.com/containerd/accelerated-container-image/blob/main/docs/EXAMPLES_CRI.md).

## Image conversion

There are 2 ways to convert images from oci format to overlaybd format, by using embedded image-convertor or using standalone userspace image-convertor respectively.

- use embedded image-convertor

```bash
# pull the source image (nerdctl or ctr)
sudo nerdctl pull registry.hub.docker.com/library/redis:6.2.1

# convert
sudo /opt/overlaybd/snapshotter/ctr obdconv registry.hub.docker.com/library/redis:6.2.1 registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# push the overlaybd image to registry, then the new converted image can be used as a remote image
sudo nerdctl push registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# remove the local overlaybd image
sudo nerdctl rmi registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new
```

- use standalone userspace image-convertor

```bash
# userspace-image-convertor will automatically pull and push images from and to the registry
sudo /opt/overlaybd/snapshotter/convertor -r registry.hub.docker.com/library/redis -i 6.2.1 -o 6.2.1_obd_new
```

## Image build

Overlaybd images can be efficiently built from overlaybd images by using the [customized buildkit](https://github.com/data-accelerator/buildkit).

### Install

```bash
https://github.com/data-accelerator/buildkit.git
# 202210 is the latest branch
git checkout 202210
make
sudo make install
```

### Run buildkitd

First, make sure the overlaybd-snapshotter and overlaybd-tcmu running.

```bash
# use containerd worker with overlaybd snapshotter
buildkitd --containerd-worker-snapshotter=overlaybd --oci-worker=false --containerd-worker=true
```

Then, write your Dockerfile and run buildctl to build images.
The `FROM` if Dockerfile must be an overlaybd image.

```bash
buildctl build \
    --frontend dockerfile.v0 \
    --local context=. \
    --local dockerfile=.  \
    --output type=image,name={new image},push=true,oci-mediatypes=true,compression=uncompressed
```

`oci-mediatypes=true` and `compression=uncompressed` are required.


## P2P

...
