# Building

This doc includes:

* [Requirements](#requirements)
* [Build from source](#build-from-source)
* [Configure](#configure)
  * [Proxy snapshotter plugin config](#proxy-snapshotter-plugin-config)
  * [containerd config](#containerd-config)
* [Run](#run)

## Requirements

* Install Go >= 1.26.x
* Install runc >= 1.0
* Install containerd >= 2.0.x
  * See [Downloads at containerd.io](https://containerd.io/downloads/).

### Build from source

You need git to checkout the source code and compile:

```bash
git clone https://github.com/containerd/accelerated-container-image.git
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

#### TCMU data-area size

The optional `maxDataAreaMB` property requests a TCMU data-area limit for each
new OverlayBD device, in MiB. Omission or `0` requests 4 MiB. Positive integer
values up to 2147483647 are accepted; the effective limit also depends on the
kernel's `target_core_user` module settings.

For example, add `"maxDataAreaMB": 64` to the snapshotter configuration to
request 64 MiB for each new device. A larger area can admit more outstanding
read data, at the cost of a larger potential memory footprint per device.
Account for the number of active devices when choosing a value.

The snapshotter sets the size before enabling a device and reads back
`attrib/max_data_area_mb` under its configfs target directory. Device creation
continues with a warning if the attribute is missing, preserving compatibility
with kernels such as unpatched Linux 4.14. On these kernels, the control write
can silently ignore the size setting, so the requested size is not guaranteed.
The warning includes the requested size and the readback error. Support is
detected from the interface rather than the kernel version to accommodate
vendor backports.

Other read errors, malformed values, and effective sizes that differ from the
request still fail device creation, including when the kernel clamps the
request to its global limit. The snapshotter does not change that global limit.

The configuration is read at startup and applies to newly created devices.
Restarting the snapshotter does not resize devices that are already mounted.
To apply a different size to an existing device, remove its workloads and
replace it through the normal device lifecycle. Rolling back the setting also
requires replacing enlarged devices to return them to the default size.

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
