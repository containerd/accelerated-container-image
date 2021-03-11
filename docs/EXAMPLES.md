# Getting Started

Before checkout the examples, make sure that you have build or install the components described in [OverlayBD](https://github.com/alibaba/overlaybd/README.md), [BUILDING](BUILDING.md) and [INSTALL](INSTALL.md).

This doc includes:

* [Setup Components](#setup-components)
  * [OverlayBD](#overlaybd)
  * [Proxy OverlayBD Snapshotter](#proxy-overlaybd-snapshotter)
* [Ondemand Pulling Image Case](#ondemand-pulling-image-case)
* [Writable OverlayBD](#writable-overlaybd)
* [Convert OCI Image into OverlayBD](#convert-oci-image-into-overlaybd)

## Setup Components

### OverlayBD

Check the overlaybd backing-store of tgt.

```bash
sudo tgtadm --lld iscsi --mode system --op show | grep -B 5 overlaybd
```

If there is no overlaybd backing-store, please checkout [OverlayBD](https://github.com/alibaba/overlaybd).

### Proxy OverlayBD Snapshotter

Start the OverlayBD snapshotter.

```bash
# in one terminal

# add config for overlaybd-config
sudo mkdir /etc/overlaybd-snapshotter
sudo cat <<-EOF | sudo tee /etc/overlaybd-snapshotter/config.json
{
        "root": "/var/lib/overlaybd/",
        "address": "/run/overlaybd-snapshotter/overlaybd.sock"
}
EOF

# run snapshotter
sudo bin/snapshotter
```

Use `ctr` to check the plugin.

```bash
# in other terminal
sudo ctr plugin ls | grep overlaybd

sudo ctr snapshot --snapshotter overlaybd ls
```

If there is no overlaybd plugin, please checkout the section [About proxy snapshotter plugin for containerd](BUILDING.md#about-proxy-snapshotter-plugin-for-containerd).

## Ondemand Pulling Image.

The containerd feature [supports target snapshot references on prepare](https://github.com/containerd/containerd/pull/3793) is released by v1.4.x.
And we provide `ctr` subcommand `rpull` as plugin to use feature to support pulling image in on-demand mode.

```bash
# you will see that the rpull doesn't pull layer data.
sudo /opt/overlaybd/bin/ctr rpull overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/redis:6.2.1_obd
```

And start a container based on this image.

```bash
$ sudo ctr run --net-host --snapshotter=overlaybd --rm -t overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/redis:6.2.1_obd demo
1:C 03 Mar 2021 04:39:31.804 # oO0OoO0OoO0Oo Redis is starting oO0OoO0OoO0Oo
1:C 03 Mar 2021 04:39:31.804 # Redis version=6.2.1, bits=64, commit=00000000, modified=0, pid=1, just started
1:C 03 Mar 2021 04:39:31.804 # Warning: no config file specified, using the default config. In order to specify a config file use redis-server /path/to/redis.conf
1:M 03 Mar 2021 04:39:31.805 # You requested maxclients of 10000 requiring at least 10032 max file descriptors.
1:M 03 Mar 2021 04:39:31.805 # Server can't set maximum open files to 10032 because of OS error: Operation not permitted.
1:M 03 Mar 2021 04:39:31.805 # Current maximum open files is 1024. maxclients has been reduced to 992 to compensate for low ulimit. If you need higher maxclients increase 'ulimit -n'.
1:M 03 Mar 2021 04:39:31.805 * monotonic clock: POSIX clock_gettime
                _._
           _.-``__ ''-._
      _.-``    `.  `_.  ''-._           Redis 6.2.1 (00000000/0) 64 bit
  .-`` .-```.  ```\/    _.,_ ''-._
 (    '      ,       .-`  | `,    )     Running in standalone mode
 |`-._`-...-` __...-.``-._|'` _.-'|     Port: 6379
 |    `-._   `._    /     _.-'    |     PID: 1
  `-._    `-._  `-./  _.-'    _.-'
 |`-._`-._    `-.__.-'    _.-'_.-'|
 |    `-._`-._        _.-'_.-'    |           http://redis.io
  `-._    `-._`-.__.-'_.-'    _.-'
 |`-._`-._    `-.__.-'    _.-'_.-'|
 |    `-._`-._        _.-'_.-'    |
  `-._    `-._`-.__.-'_.-'    _.-'
      `-._    `-.__.-'    _.-'
          `-._        _.-'
              `-.__.-'

1:M 03 Mar 2021 04:39:31.808 # WARNING: The TCP backlog setting of 511 cannot be enforced because /proc/sys/net/core/somaxconn is set to the lower value of 128.
1:M 03 Mar 2021 04:39:31.808 # Server initialized
1:M 03 Mar 2021 04:39:31.808 # WARNING overcommit_memory is set to 0! Background save may fail under low memory condition. To fix this issue add 'vm.overcommit_memory = 1' to /etc/sysctl.conf and then reboot or run the command 'sysctl vm.overcommit_memory=1' for this to take effect.
1:M 03 Mar 2021 04:39:31.810 * Ready to accept connections
```

## Convert OCI Image into OverlayBD

OverlayBD image convertor helps to convert a normal image to OverlayBD-format remote image.

```bash
# pull the source image
sudo ctr content fetch overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/redis:6.2.1

# convert
sudo /opt/overlaybd/bin/ctr obdconv overlaybd-registry.cn-hangzhou.cr.aliyuncs.com/example/redis:6.2.1 localhost:5000/redis:6.2.1_obd

# run
ctr run --net-host --snapshotter=overlaybd --rm -t localhost:5000/redis:6.2.1_obd demo

1:C 03 Mar 2021 04:50:12.853 # oO0OoO0OoO0Oo Redis is starting oO0OoO0OoO0Oo
1:C 03 Mar 2021 04:50:12.853 # Redis version=6.2.1, bits=64, commit=00000000, modified=0, pid=1, just started
1:C 03 Mar 2021 04:50:12.853 # Warning: no config file specified, using the default config. In order to specify a config file use redis-server /path/to/redis.conf
1:M 03 Mar 2021 04:50:12.854 # You requested maxclients of 10000 requiring at least 10032 max file descriptors.
1:M 03 Mar 2021 04:50:12.855 # Server can't set maximum open files to 10032 because of OS error: Operation not permitted.
1:M 03 Mar 2021 04:50:12.855 # Current maximum open files is 1024. maxclients has been reduced to 992 to compensate for low ulimit. If you need higher maxclients increase 'ulimit -n'.
1:M 03 Mar 2021 04:50:12.855 * monotonic clock: POSIX clock_gettime
                _._
           _.-``__ ''-._
      _.-``    `.  `_.  ''-._           Redis 6.2.1 (00000000/0) 64 bit
  .-`` .-```.  ```\/    _.,_ ''-._
 (    '      ,       .-`  | `,    )     Running in standalone mode
 |`-._`-...-` __...-.``-._|'` _.-'|     Port: 6379
 |    `-._   `._    /     _.-'    |     PID: 1
  `-._    `-._  `-./  _.-'    _.-'
 |`-._`-._    `-.__.-'    _.-'_.-'|
 |    `-._`-._        _.-'_.-'    |           http://redis.io
  `-._    `-._`-.__.-'_.-'    _.-'
 |`-._`-._    `-.__.-'    _.-'_.-'|
 |    `-._`-._        _.-'_.-'    |
  `-._    `-._`-.__.-'_.-'    _.-'
      `-._    `-.__.-'    _.-'
          `-._        _.-'
              `-.__.-'

1:M 03 Mar 2021 04:50:12.858 # WARNING: The TCP backlog setting of 511 cannot be enforced because /proc/sys/net/core/somaxconn is set to the lower value of 128.
1:M 03 Mar 2021 04:50:12.858 # Server initialized
1:M 03 Mar 2021 04:50:12.858 # WARNING overcommit_memory is set to 0! Background save may fail under low memory condition. To fix this issue add 'vm.overcommit_memory = 1' to /etc/sysctl.conf and then reboot or run the command 'sysctl vm.overcommit_memory=1' for this to take effect.
1:M 03 Mar 2021 04:50:12.862 * Ready to accept connections
```
