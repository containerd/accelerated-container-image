# Getting Started

Before checkout the examples, make sure that you have built or installed the components described in [overlaybd](https://github.com/containerd/overlaybd/blob/main/README.md) and [BUILDING](BUILDING.md).

This doc includes:

- [Getting Started](#getting-started)
  - [Check Components](#check-components)
    - [Overlaybd](#overlaybd)
    - [Proxy overlaybd snapshotter](#proxy-overlaybd-snapshotter)
  - [Ondemand Pulling Image](#ondemand-pulling-image)
  - [Convert OCI Image into overlaybd](#convert-oci-image-into-overlaybd)

## Check Components

### Overlaybd

Check whether the `overlaybd-tcmu` is running.

Overlaybd-snapshotter v1.0.1+ requires overlaybd-tcmu v1.0.4+.

For any problems, please checkout [overlaybd](https://github.com/containerd/overlaybd).

### Proxy overlaybd snapshotter

First, you should make sure that '/etc/containerd/config.toml' has been changed and contains the following contents like this:

```bash
[proxy_plugins.overlaybd]
    type = "snapshot"
    address = "/run/overlaybd-snapshotter/overlaybd.sock"
```

Use `ctr` to check the plugin.

```bash
# in other terminal
sudo ctr plugin ls | grep overlaybd

sudo ctr snapshot --snapshotter overlaybd ls
```

If there is no overlaybd plugin, please checkout [BUILDING](BUILDING.md).

## Ondemand Pulling Image

The containerd feature [supports target snapshot references on prepare](https://github.com/containerd/containerd/pull/3793) is released by v1.4.x.

__[Recommend]__ Now we support to start an overlaybd container by [`nerdctl`](https://github.com/containerd/nerdctl)[(#603)](https://github.com/containerd/nerdctl/pull/603).

```bash
$ sudo nerdctl run --net host -it --rm --snapshotter=overlaybd registry.hub.docker.com/overlaybd/redis:6.2.1_obd

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

And also, you can use `ctr` to run this image. However, you should first use the subcommand `rpull` in the customized `bin/ctr` to pull image
```bash
# you will see that the rpull doesn't pull layer data.
sudo bin/ctr rpull registry.hub.docker.com/overlaybd/redis:6.2.1_obd
# start the container
sudo ctr run --net-host --snapshotter=overlaybd --rm -t registry.hub.docker.com/overlaybd/redis:6.2.1_obd demo
```

After container launched success, we could see a new block device (sdb) has been created, and its mount-point is the lowerdir of overlayfs.
```bash
$ sudo lsblk
NAME        MAJ:MIN RM   SIZE RO TYPE MOUNTPOINT
sdb           8:16   0   256G  1 disk /var/lib/overlaybd/snapshots/52/block/mountpoint
```


## Convert OCI Image into overlaybd

Overlaybd image convertor helps to convert a normal image to overlaybd-format remote image.

```bash
# pull the source image
sudo nerdctl pull registry.hub.docker.com/library/redis:6.2.1

# convert
sudo bin/ctr obdconv registry.hub.docker.com/library/redis:6.2.1 registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# push the overlaybd image to registry, then the new converted image can be used as a remote image
sudo nerdctl push registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# remove the local overlaybd image
sudo nerdctl rmi registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# run
sudo nerdctl run --net host --rm --snapshotter=overlaybd registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

1:C 03 Mar 2021 04:50:12.853 # oO0OoO0OoO0Oo Redis is starting oO0OoO0OoO0Oo
1:C 03 Mar 2021 04:50:12.853 # Redis version=6.2.1, bits=64, commit=00000000, modified=0, pid=1, just started
1:C 03 Mar 2021 04:50:12.853 # Warning: no config file specified, using the default config. In order to specify a config file use redis-server /path/to/redis.conf
1:M 03 Mar 2021 04:50:12.854 # You requested maxclients of 10000 requiring at least 10032 max file descriptors.
...
......
```