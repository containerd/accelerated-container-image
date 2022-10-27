# Quickstart Guide

This guide helps to config and run the common case of overlaybd image service.

- [Install](#install)
  - [overlaybd-snapshotter](#overlaybd-snapshotter)
  - [overlaybd-tcmu](#overlaybd-tcmu)
- [Configuration](#configuration)
  - [containerd](#containerd)
  - [Authentication](#authentication)
- [Run overlaybd images](#run-overlaybd-images)
- [Image conversion](#image-conversion)
- [Image build](#image-build)
- [P2P](#p2p)

## Install

There are two components to be installed, overlaybd-snapshotter and overlaybd-tcmu. They are located in two separate repositries.
- overlaybd-snapshotter is in this repositry
- overlaybd-tcmu is in https://github.com/containerd/overlaybd.

### overlaybd-snapshotter

Users can compile the latest code to install or download the [release](https://github.com/containerd/accelerated-container-image/releases).

#### Compile from source

Install dependencies:
- golang 1.16+

Run the following commands to build:
```bash
git clone https://github.com/containerd/accelerated-container-image.git
cd accelerated-container-image
make
sudo make install
```

#### Download release

After download, copy the binaries to `/opt/overlaybd/snapshotter`.

In this way, the config file is not automatically installed, users have to be setup manually.

#### Config

The config file is `/etc/overlaybd-snapshotter/config.json`. Please create the file if not exists.

```json
{
    "root": "/var/lib/overlaybd/",
    "address": "/run/overlaybd-snapshotter/overlaybd.sock"
}
```
`root` is the root directory to store snapshots, `address` is the socket address used to connect withcontainerd.

#### Start service

Run the `/opt/overlaybd/snapshotter/overlaybd-snapshotter` binary or start it as a service by enable and start [overlaybd-snapshotter.service](https://github.com/containerd/accelerated-container-image/blob/main/script/overlaybd-snapshotter.service).

If installed from source, please run the following to start service.
```bash
sudo systemctl enable /opt/overlaybd/snapshotter/overlaybd-snapshotter.service
sudo systemctl start overlaybd-snapshotter
```

### overlaybd-tcmu

Users can compile the latest code to install or download the [release](https://github.com/containerd/overlaybd/releases).

#### Compile from source

Install dependencies:
- cmake 3.15+
- gcc/g++ 7+
- development dependencies:
    - CentOS/Fedora: sudo yum install libaio-devel libcurl-devel openssl-devel libnl3-devel
    - Debian/Ubuntu: sudo apt install libcurl4-openssl-dev libssl-dev libaio-dev libnl-3-dev libnl-genl-3-dev libgflags-dev

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
    sudo /opt/overlaybd/snapshotter/ctr -u {user}:{pass} rpull registry.hub.docker.com/overlaybd/redis:6.2.1_obd

    # run by ctr run
    sudo ctr run --net-host --snapshotter=overlaybd --rm -t registry.hub.docker.com/overlaybd/redis:6.2.1_obd demo
    ```

- usr k8s/cri

    Run with k8s or crictl, refer to [EXAMPLES_CRI](https://github.com/containerd/accelerated-container-image/blob/main/docs/EXAMPLES_CRI.md).

## Image conversion

Images can be convert from oci format to overlaybd format by the following commands.

```bash
# pull the source image (nerdctl or ctr)
sudo nerdctl pull registry.hub.docker.com/library/redis:6.2.1

# convert
sudo /opt/overlaybd/snapshotter/ctrobdconv registry.hub.docker.com/library/redis:6.2.1 registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# push the overlaybd image to registry, then the new converted image can be used as a remote image
sudo nerdctl push registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# remove the local overlaybd image
sudo nerdctl rmi registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new
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