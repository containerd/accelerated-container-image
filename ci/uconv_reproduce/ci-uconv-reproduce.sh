#!/bin/bash

convertor="/opt/overlaybd/snapshotter/convertor"
images=("centos:centos7.9.2009" "ubuntu:22.04" "redis:7.2.3" "wordpress:6.4.2" "nginx:1.25.3")
repo="registry.hub.docker.com/overlaybd"
ci_base=$(pwd)

result=0

for image in ${images[@]}
do
    img=${image%%":"*}
    tag=${image##*":"}
    echo ${img} ${tag}

    o_tag="${tag}_obd"
    tmp_dir="${ci_base}/tmp_conv_${image/:/_}"

    rm -rf ${tmp_dir}
    mkdir -p ${tmp_dir}

    ${convertor} -r ${repo}/${img}  \
        --reserve --no-upload --dump-manifest \
        -i ${tag} -o ${o_tag} -d ${tmp_dir} &>${tmp_dir}/convert.out

    prefix=$(date +%Y%m%d%H%M%S)
    files=("manifest" "config")
    conv_res=0
    for file in ${files[@]}
    do
        fn="${file}.json"
        diff ${tmp_dir}/${fn} ${ci_base}/${img}/${fn}
        ret=$?
        if [[ ${ret} -eq 0 ]]; then
            echo "${prefix} ${img} ${file} consistent"
        else
            echo "${prefix} ${img} ${file} diff"
            conv_res=1
        fi
    done

    if [[ ${conv_res} -eq 1 ]]; then
        cat ${tmp_dir}/convert.out | sed 's/\\n/\n/g'
        result=1
    fi
    rm -rf ${tmp_dir}
done

exit ${result}
