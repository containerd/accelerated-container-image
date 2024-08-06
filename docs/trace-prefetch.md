## Prefetch - Overview

Cache has been playing an important role in the whole architecture of ACI's [I/O flow](images/image-flow.jpg "image data flow"). When there is no cache (container cold start), however, the backend storage engine will still need to visit Registry frequently, and temporarily.

Prefetch is a common mechanism to avoid this situation. As it literally suggests, the key is to retrieve data in advance, and save them into cache.

There are many ways to do prefetch, for instance, we can simply read extra data beyond the designated range of a Registry blob. That might be called as Expand Prefetch, and the expand ratio could be 2x, 4x, or even higher, if our network bandwidth is sufficient.

Another way is to [prioritize files and use landmarks](https://github.com/containerd/stargz-snapshotter/blob/master/docs/stargz-estargz.md#prioritized-files-and-landmark-files), which is already adopted in Google's stargz. The storage engine runtime will prefetch the range where prioritized files are contained. And finally this information will be leveraged for increasing cache hit ratio and mitigating read overhead.

In this article we are about to introduce two prefetch modes in overlayBD. One is to set prioritized files, another is a new prefetch mechanism based on time sequenced I/O patterns (trace).
These two mechanisms have been integrated as a feature into `ctr record-trace` command.

## Prefetch Mode

### Prioritize Files

Setting prioritized files is a simple way to improve container's cold start time. It is suitable for the condition where the target files needed be fully loaded.

When overlaybd device has been created, it will get prioritized files from the priority_list and analyze the filesystem via libext4 before mounting, then download the target files to overalybd's cache.

**Only support images based on EXT4 filesystem**

The priority list is a simple text file, each line contains a file path like follow:
```bash
## cat /tmp/priority_list.txt
/usr/bin/containerd
/usr/bin/nerdctl
/opt/cni/dhcp
/opt/cni/vlan
```


### Trace Prefetch

Since every single I/O request happens on user's own filesystem will eventually be mapped into one overlaybd's layer blob, we can then record all I/Os from the layer blob's perspective, and replay them later. That's why we call it Trace Prefetch.

Trace prefetch is time based, and it has greater granularity and predication accuracy than stargz. We don't mark a file, because user app might only need to read a small part of it in the beginning, simply prefetching the whole file would be less efficient. Instead, we replay the trace, by the exact I/O records that happened before. Each record contains only necessary information, such as the offset and length of the blob being read.

**!! Note !!**

Both priority list and I/O trace are stored as an independent image layer, and MUST always be the uppermost one. Neither image manifest nor container snapshotter needs to know if it is a trace layer, snapshotter just downloads and extracts it as usual. The overlaybd backstore MUST recognize trace layer, and replay it accordingly.

## Terminology

### Record

Recording is to run a temporary container based on the target image, persist I/O records during startup, and then dump them into a trace blob. The trace blob will be chained, and become the top layer.

Recording functionality SHOULD be integrated into container's build (compose) system, and MUST have a parameter to indicate how long the user wishes to record. After timeout, the build system MUST stop the running container, so the recording terminates as well.

The container could be either stateless or stateful. CNI is enabled by default to provide an isolated network, so that the recording container is unlikely to cause unexpected consequences in production environment.

When building a new image from a base image, the old trace layer (if exists in the base image) MUST be removed. New trace layer might be added later, if recording is desired.

### Push

Push command will save both data layer and trace layer to Registry. The trace layer is transparent to the push command.

### Replay

After Recording and Pushing, users could pull and run the specific image somewhere else. Snapshotter's storage backend SHOULD load the trace blob, and replay I/O records for each layer blob.

## Example Usage

The example usage of building a new image with trace layer would be as follows:
```
bin/ctr rpull --download-blobs <image>

## trace prefetch
bin/ctr record-trace --time 20 <image> <image_with_trace>

## prioritized files
bin/ctr record-trace --priority_list <path/to/filelist> <image> <image_with_trace>

ctr i push <image_with_trace>
```

Note the `<image>` must be in overlaybd format. A temporary container will be created and do the recording. The recording progress will be terminated by either timeout, or user signals.

Due to current limitations, this command might ask you remove the old image locally, in order to prepare a clean environment for the recording.

We also support triggering `record` and `replay` process by passing labels through `--snapshotter-label` when `run` container, which needing containerd/ctr 1.6+.
```
ctr run --snapshotter=overlaybd --snapshotter-label containerd.io/snapshot/overlaybd/record-trace=yes --snapshotter-label containerd.io/snapshot/overlaybd/record-trace-path=<trace_file> --rm -t <obd_image> demo
```
Setting label `containerd.io/snapshot/overlaybd/record-trace` to `yes` to enable this feature, setting label `containerd.io/snapshot/overlaybd/record-trace-path` to `<trace_file>` for tracing.

Noting that, `<trace_file>` should exist before running. If `<trace_file>` is `0` sized, `record` process will be triggered, otherwise will be `replay`. `record` process will stop when overlaybd device stops, and `<trace_file>` will be generated.

## Performance

Measure the service available time (in seconds) of a Wordpress container. The testing host is deployed in a different region with registry service. The network bandwidth is 25Mbps.

| | **With cache** | **Without cache (cold start)** |
| :----: | :----: | :----: |
| **With trace prefetch** | 4.981s | 15.253s |
| **Without trace prefetch** | 5.259s | 60.511s |
