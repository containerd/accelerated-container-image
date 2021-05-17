## Overview

Cache has been playing an important role in the whole architecture of ACI's [I/O flow](docs/images/image-flow.jpg "image data flow"). When there is no cache (container cold start), however, the backend storage engine will still need to visit Registry frequently, and temporarily.

Prefetch is a common mechanism to avoid this situation. As it literally suggests, the key is to retrieve data in advance, and save them into cache.

There are many ways to do prefetch, for instance, we can simply read extra data beyond the designated range of a Registry blob. That might be called as Expand Prefetch, and the expand ratio could be 2x, 4x, or even higher, if our network bandwidth is sufficient.

Another way is to [prioritize files and use landmarks](https://github.com/containerd/stargz-snapshotter/blob/master/docs/stargz-estargz.md#prioritized-files-and-landmark-files), which is already adopted in Google's stargz. The storage engine runtime will prefetch the range where prioritized files are contained. And finally this information will be leveraged for increasing cache hit ratio and mitigating read overhead.

In this article we are about to introduce a new prefetch mechanism based on time sequenced I/O patterns (trace). This mechanism has been integrated as a feature into `ctr record-trace` command.

## Trace Prefetch

Since every single I/O request happens on user's own filesystem will eventually be mapped into one overlaybd's layer blob, we can then record all I/Os from the layer blob's perspective, and replay them later. That's why we call it Trace Prefetch.

Trace prefetch is time based, and it has greater granularity and predication accuracy than stargz. We don't mark a file, because user app might only need to read a small part of it in the beginning, simply prefetching the whole file would be less efficient. Instead, we replay the trace, by the exact I/O records that happened before. Each record contains only necessary information, such as the offset and length of the blob being read.

Trace is stored as an independent image layer, and MUST always be the uppermost one. Neither image manifest nor container snapshotter needs to know if it is a trace layer, snapshotter just downloads and extracts it as usual. The overlaybd backstore MUST recognize trace layer, and replay it accordingly.

## Terminology

### Record

Recording is to run a container based on the target image, persist I/O records during startup, and then dump them into a trace blob. The trace blob will be chained, and become the top layer.

Recording functionality SHOULD be integrated into container's build (compose) system, and MUST have a parameter to indicate how long the user wishes to record. After timeout, the build system MUST stop the running container, so the recording terminates as well.

It is user's responsibility to ensure the container is idempotent or stateless, in other words, it SHOULD be able to start anywhere and anytime without causing unexpected consequences.

When building a new image from a base image, the old trace layer (if exists in the base image) MUST be removed. New trace layer might be added later, if recording is desired.

### Push

Push command will save both data layer and trace layer to Registry. The trace layer is transparent to the push command.

### Replay

After Recording and Pushing, users could pull and run the specific image somewhere else. Snapshotter's storage backend SHOULD load the trace blob, and replay I/O records for each layer blob.

## Example Usage

The example usage of building a new image with trace layer would be as follows:
```
ctr record-trace --time 20 <old_image> <local>

ctr push <new_image> <local>
```

Note the old image must be in overlaybd format. A temporary container will be created and do the recording. The recording progress will be terminated by either timeout, or user signals.

Due to current limitations, this command might ask you remove the old image locally, in order to prepare a clean environment for the recording.

## Performance

Measure the service available time (in seconds) of a Wordpress container. The testing host is deployed in a different region with registry service. The network bandwidth is 25Mbps.

| | **With cache** | **Without cache (cold start)** |
| :----: | :----: | :----: |
| **With trace layer** | 4.981s | 15.253s |
| **Without trace layer** | 5.259s | 60.511s |