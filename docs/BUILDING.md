# Building

This doc includes:

* [Requirements](#requirements)
* [Build from source](#build-from-source)
* [Configure](#configure)
  * [Proxy snapshotter plugin config](#proxy-snapshotter-plugin-config)
  * [containerd config](#containerd-config)
* [Run](#run)

## Requirements

* Install Go >= 1.15.x
* Install runc >= 1.0
* Install containerd >= 1.4.x
  * See [Downloads at containerd.io](https://containerd.io/downloads/).

### Build from source

You need git to checkout the source code and compile:

```bash
git clone https://github.com/alibaba/accelerated-container-image.git
cd accelerated-container-image
make
```

The snapshotter and ctr plugin are generated in `bin`.

## Configure

### proxy snapshotter plugin config

```bash
sudo mkdir /etc/overlaybd-snapshotter
sudo cat <<-EOF | sudo tee /etc/overlaybd-snapshotter/config.json
{
    "root": "/var/lib/overlaybd/",
    "address": "/run/overlaybd-snapshotter/overlaybd.sock"
}
EOF
```

### containerd config

```bash
sudo cat <<-EOF | sudo tee --append /etc/containerd/config.toml

[proxy_plugins.overlaybd]
    type = "snapshot"
    address = "/run/overlaybd-snapshotter/overlaybd.sock"
EOF
```

## Run

```bash
# run snapshotter plugin
sudo bin/overlaybd-snapshotter

# restart containerd
sudo systemctl restart containerd
```