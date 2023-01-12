# Writable layer

Overlaybd writable/container layer can be used for image build/conversion, and container runtime.


There are two kinds of writable layer, `append-only` based and `sparse-file` based:

* **append-only**

    It is a writable layer in the form of log-structure that data is appended to the end of the data file with index update. It has high performance but the size will increase without gc (not implemented yet). It is suitable for image conversion and image building.

* **sparse-file**

    Is is a writable layer based on sparse file. It is suitable for container runtime.

    *This feature is incomming.*


There are two kinds of mount mode, `dir` mod and `dev` mod:

* **dir**

    It returns a bind mount when calling snapshotter `Mounts`, the image with writable layer will be mount to a dir before `Mounts` return. This dir can be used as rootfs directly.

* **dev**

    It returns a mount of a writable device with fs type parameter when calling snapshotter `Mounts`. It is up to the caller to decide when and where to mount the device. This device can be passed to a secure container or a vm runtime through virtio-blk.


## Usage

* for containerd/ctr 1.6+

    Just pass the parameter through `--snapshotter-label`, by setting `dev` or `dir` to `containerd.io/snapshot/overlaybd.writable`. [#5660](https://github.com/containerd/containerd/pull/5660)

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

* for proxy snapshotter plugin config

    ```json
    {
        "root": "/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd",
        "address": "/run/overlaybd-snapshotter/overlaybd.sock",
        "rwMode": "dev", // overlayfs, dir or dev
        "writableLayerType": "sparse" // append or sparse
    }
    ```