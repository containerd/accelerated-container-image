# Writable layer

Overlaybd can be used as writable/container layer, which can be used for image build/conversion, and container runtime.


There are two kinds of writable layer, append-only based and sparse-file based:

* **append-only**

    It is a writable layer in the form of log-structure that data is append to the end of the data file. It has high performance but the size will increase without gc (not implemented yet). It is suitable for image building.

* **sparse-file**

    Is is a writable layer based on sparse file. It is suitable for container runtime.

    *This feature is incomming.*


There are two kinds of mount mode, dir mod and dev mod:

* **dir**

    It returns a bind mount when calling snapshotter `Mounts`, the image with writable layer will be mount to a dir before `Mounts` return. This dir can be used as rootfs directly.

* **dev**

    It returns a mount of a writable device with fs type when calling snapshotter `Mounts`. It is up to the caller to decide when and where to mount the device. This device can be passed to a secure container or a vm runtime through virtio-blk.


## Usage

* for containerd/ctr 1.6+

    Just pass the parameter through `--snapshotter-label`, by passing `dev` or `dir` to `containerd.io/snapshot/overlaybd.writable support`.

    ```bash
    ctr run --net-host --snapshotter=overlaybd --rm -t --snapshotter-label containerd.io/snapshot/overlaybd.writable=dev registry.hub.docker.com/overlaybd/redis:6.2.1_obd test_rw
    ```


* for containerd api

    ```go
	snOpts := snapshots.WithLabels(map[string]string{"containerd.io/snapshot/overlaybd.writable": "dev"})
	redis, err := client.NewContainer(context, "redis-master",
		containerd.WithSnapshotter("overlaybd"),
		containerd.WithNewSnapshot("redis-rootfs", image, snOpts),
		containerd.WithNewSpec(oci.WithImageConfig(image)),
	)
    ```