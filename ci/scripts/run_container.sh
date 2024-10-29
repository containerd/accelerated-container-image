#!/bin/bash
#
# rpull and run on-demand

set -x

image=$1
container_name=${2:-test}

exit_code=0

/opt/overlaybd/snapshotter/ctr rpull "${image}"
if ! ctr run -d --net-host --snapshotter=overlaybd "${image}" "${container_name}"; then
  exit_code=1
fi
lsblk
if ! ctr t ls | grep "${container_name}"; then
  exit_code=1
fi
ctr t kill -s 9 "${container_name}" && sleep 5s && ctr t ls
ctr c rm "${container_name}" && ctr c ls
ctr i rm "${image}"

if [[ ${exit_code} -ne 0 ]]; then
  cat /var/log/overlaybd.log
fi

exit ${exit_code}
