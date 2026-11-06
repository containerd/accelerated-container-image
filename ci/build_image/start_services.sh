#!/bin/bash

set -e

# cgroup v2: enable nesting
# https://github.com/moby/moby/blob/38805f20f9bcc5e87869d6c79d432b166e1c88b4/hack/dind#L28-L38
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    # move the processes from the root group to the /init group,
    # otherwise writing subtree_control fails with EBUSY.
    # An error during moving non-existent process (i.e., "cat") is ignored.
    mkdir -p /sys/fs/cgroup/init
    xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs || :
    # enable controllers
    sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
        > /sys/fs/cgroup/cgroup.subtree_control
fi

# Load kernel modules before starting their consumers, so that kernel
# features (e.g. overlay "index=off") are available when services probe them.
/sbin/modprobe target_core_user
/sbin/modprobe overlay

/opt/overlaybd/bin/overlaybd-tcmu &

/opt/overlaybd/snapshotter/overlaybd-snapshotter &>/var/log/overlaybd-snapshotter.log &

/usr/bin/containerd &>/var/log/containerd.log &
