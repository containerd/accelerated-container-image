#!/bin/bash

if [ "$#" != "1" ]; then
        echo "Need to input image"
        exit 1
fi

IMAGE="$1"
sudo ctr task kill wordpress_demo
sleep 2   # wait enough time for container to stop
sudo ctr task delete wordpress_demo
sudo ctr container delete wordpress_demo
sudo ctr image remove $IMAGE
