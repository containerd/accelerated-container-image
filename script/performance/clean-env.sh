#!/bin/bash

set -e

echo "Stop running containers ..."
for i in `sudo ctr task ls | grep -E '\sRUNNING' | awk '{print $1}'`; do
	sudo ctr task kill $i
	while true; do
		sleep 1
		if sudo ctr task ls | grep -E '\sSTOPPED' | grep $i > /dev/null; then
			break
		fi
	done
done

echo "Remove containers ..."
for i in `sudo ctr task ls -q`; do
	sudo ctr task delete $i
done
for i in `sudo ctr container ls -q`; do
	sudo ctr container delete $i
done

echo "Remove images ..."
for i in `sudo ctr image ls -q`; do
	sudo ctr image rm --sync $i
done
sleep 3

echo "Clean registry cache and page cache ..."
sudo rm -rf /opt/overlaybd/registry_cache/*
sudo bash -c 'echo 1 > /proc/sys/vm/drop_caches'

echo "Restarting overlaybd backstore ..."
sudo systemctl restart overlaybd-tcmu
