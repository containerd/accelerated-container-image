#!/bin/bash

set -e

if [ "$#" != "1" ]; then
	echo "Need to input image"
	exit 1
fi

IMAGE="$1"

sudo ctr task kill wordpress_demo
while true; do
	if sudo ctr task ls | grep STOPPED | grep wordpress_demo > /dev/null; then
		break
	fi
	sleep 1
done
sudo ctr task delete wordpress_demo
sudo ctr container delete wordpress_demo
sudo ctr image remove --sync $IMAGE
sudo bash -c 'echo 1 > /proc/sys/vm/drop_caches'
