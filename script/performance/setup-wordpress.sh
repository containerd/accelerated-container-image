#!/bin/bash

if [ "$#" != "1" ]; then
        echo "Need to input image"
        exit 1
fi

IMAGE="$1"
sudo ../../bin/ctr rpull $IMAGE
sudo ctr run --net-host --snapshotter=overlaybd -d $IMAGE wordpress_demo

while true; do
        sleep 0.1
        if nc -w 1 localhost 80 < /dev/null &> /dev/null; then
                break
        fi
done

wget -q -t 1000 --waitretry=0.1 -w 0 --retry-connrefused -O /dev/null http://127.0.0.1/wp-admin/setup-config.php

echo "Service Available"
