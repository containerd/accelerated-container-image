#!/bin/bash

apt update && apt install -y python3 jq

convertor="/opt/overlaybd/snapshotter/convertor"
images=("centos:centos7.9.2009" "ubuntu:22.04" "redis:7.2.3" "wordpress:6.4.2" "nginx:1.25.3")
registry="localhost:5000"
ci_base=$(pwd)

result=0

for image in "${images[@]}"
do
    img=${image%%":"*}
    tag=${image##*":"}
    echo "${img} ${tag}"

    workspace="${ci_base}/workspace_${image/:/_}"

    rm -rf "${workspace}"
    mkdir -p "${workspace}"

    tag_obd="${tag}_overlaybd"
    tag_turbo="${tag}_turbo"
    tag_turbo_erofs="${tag}_turbo_erofs"
    manifest_obd="${workspace}/manifest.json"
    manifest_turbo="${workspace}/manifest-turbo.json"
    manifest_turbo_erofs="${workspace}/manifest-turbo-erofs.json"
    config_obd="${workspace}/config.json"
    config_turbo="${workspace}/config-turbo.json"
    config_turbo_erofs="${workspace}/config-turbo-erofs.json"
    output_obd="${workspace}/convert.overlaybd.out"
    output_turbo="${workspace}/convert.turbo.out"
    output_turbo_erofs="${workspace}/convert.turbo.erofs.out"

    ${convertor} -r "${registry}/${img}" -i "${tag}" --overlaybd "${tag_obd}" -d "${workspace}/overlaybd_tmp_conv" &> "${output_obd}"
    curl -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json" -o "${manifest_obd}" "https://${registry}/v2/${img}/manifests/${tag_obd}" &> /dev/null
    configDigest=$(jq '.config.digest' "${manifest_obd}")
    configDigest=${configDigest//\"/}
    curl -o "${config_obd}" "https://${registry}/v2/${img}/blobs/${configDigest}" &> /dev/null

    ${convertor} -r "${registry}/${img}" -i "${tag}" --turboOCI "${tag_turbo}" -d "${workspace}/turbo_tmp_conv" &> "${output_turbo}"
    curl -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json" -o "${manifest_turbo}" "https://${registry}/v2/${img}/manifests/${tag_turbo}" &> /dev/null
    configDigest=$(jq '.config.digest' "${manifest_turbo}")
    configDigest=${configDigest//\"/}
    curl -o "${config_turbo}" "https://${registry}/v2/${img}/blobs/${configDigest}" &> /dev/null

    ${convertor} -r "${registry}/${img}" -i "${tag}" --turboOCI "${tag_turbo_erofs}" --fstype erofs -d "${workspace}/turbo_erofs_tmp_conv" &> "${output_turbo_erofs}"
    curl -s -H "Accept: application/vnd.docker.distribution.manifest.v2+json" "https://${registry}/v2/${img}/manifests/${tag_turbo_erofs}"
    curl -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json" -o "${manifest_turbo_erofs}" "https://${registry}/v2/${img}/manifests/${tag_turbo_erofs}" &> /dev/null
    configDigest=$(jq '.config.digest' "${manifest_turbo_erofs}")
    configDigest=${configDigest//\"/}
    curl -o "${config_turbo_erofs}" "https://${registry}/v2/${img}/blobs/${configDigest}" &> /dev/null

    prefix=$(date +%Y%m%d%H%M%S)

    mode=("manifest" "config" "manifest" "config" "manifest" "config")
    actual=("${manifest_obd}" "${config_obd}" "${manifest_turbo}" "${config_turbo}" "${manifest_turbo_erofs}" "${config_turbo_erofs}")
    expected=("${ci_base}/${img}/manifest.json" "${ci_base}/${img}/config.json" "${ci_base}/${img}/manifest-turbo.json" "${ci_base}/${img}/config-turbo.json" "${ci_base}/${img}/manifest-turbo-erofs.json" "${ci_base}/${img}/config-turbo-erofs.json")

    conv_res=0
    n=${#mode[@]}
    for ((i=0; i<n; i++)); do
        python3 compare_layers.py ${mode[$i]} ${actual[$i]} ${expected[$i]}
        ret=$?
        if [[ ${ret} -eq 0 ]]; then
            echo "${prefix} ${img} ${expected[$i]} consistent"
        else
            echo "${prefix} ${img} ${expected[$i]} diff"
            conv_res=1
        fi
    done

    if [[ ${conv_res} -eq 1 ]]; then
        sed 's/\\n/\n/g' < "${output_obd}"
        sed 's/\\n/\n/g' < "${output_turbo}"
        result=1
    fi
done

exit ${result}