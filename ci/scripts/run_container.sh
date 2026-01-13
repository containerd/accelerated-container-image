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

snapshot_id=""
config_path=""
if [[ ${exit_code} -eq 0 ]]; then
  snapshot_id=$(ctr c info "${container_name}" | grep -oP '"SnapshotKey":\s*"\K[^"]+')
  if [[ -n "${snapshot_id}" ]]; then
    config_path="/var/lib/overlaybd/snapshots/${snapshot_id}/block/config.v1.json"
    if [[ -f "${config_path}" ]]; then
      echo "Found config file: ${config_path}"
      cp "${config_path}" /tmp/test_config.v1.json
    else
      echo "Warning: config file not found at ${config_path}"
    fi
  else
    echo "Warning: could not get snapshot ID"
  fi
fi

ctr t kill -s 9 "${container_name}" && sleep 5s && ctr t ls
ctr c rm "${container_name}" && ctr c ls
ctr i rm "${image}"

if [[ -f /tmp/test_config.v1.json && -n "${snapshot_id}" ]]; then
  echo "Testing overlaybd-attacher attach..."
  if /opt/overlaybd/snapshotter/overlaybd-attacher attach --id "${snapshot_id}" --config /tmp/test_config.v1.json; then
    echo "overlaybd-attacher attach succeeded"

    echo "Testing overlaybd-attacher detach..."
    if /opt/overlaybd/snapshotter/overlaybd-attacher detach --id "${snapshot_id}"; then
      echo "overlaybd-attacher detach succeeded"
    else
      echo "overlaybd-attacher detach failed"
      exit_code=1
    fi
  else
    echo "overlaybd-attacher attach failed"
    exit_code=1
  fi
  rm -f /tmp/test_config.v1.json
fi

if [[ ${exit_code} -ne 0 ]]; then
  cat /var/log/overlaybd.log
fi

exit ${exit_code}
