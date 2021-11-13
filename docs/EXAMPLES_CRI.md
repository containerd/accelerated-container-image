# Getting Started

## Check Components

Before checkout the cri examples, make sure that you have built or installed the components described in [overlaybd](https://github.com/containerd/overlaybd/README.md) and [BUILDING](BUILDING.md).
You can check your overlaybd components as described in [EXAMPLES](EXAMPLES.md#check-components).

## Containerd config

Config the plugin cri for containerd. And make sure `cri` is not listed in `disabled_plugins` of containerd config file.

```bash
sudo cat <<-EOF | sudo tee --append /etc/containerd/config.toml

[plugins.cri]
    [plugins.cri.containerd]
        snapshotter = "overlaybd"
        disable_snapshot_annotations = false
EOF
```

## Pod and container config

The example pod-config and container-config.

```bash
sudo cat <<-EOF | sudo tee pod-config.yaml
metadata:
  attempt: 1
  name: redis-obd
  namespace: default
log_directory: /tmp
linux:
  security_context:
    namespace_options:
      network: 2
EOF
```

```bash
sudo cat <<-EOF | sudo tee container-config.yaml
metadata:
  name: redis-obd-container
image:
  image: registry.hub.docker.com/overlaybd/redis:6.2.1_obd
log_path: redis.container.log
EOF
```

## Create container by crictl

```bash
crictl run container-config.yaml pod-config.yaml
```
