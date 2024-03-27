#!/bin/bash
# This script is a sample workload for the convertor that demonstrates the use of the manifest / layer caching for
# deduplication.
# Validation -> All obd images should be the same with the exception of the layer cache test images

# This sample results in the following registry layout:
# $registry
#   ├── $repository
#   │   ├── $inputtag                                   // Original image
#   │   ├── $inputtag-extra-layer                       // Original image with one added random extra layer
#   │   ├── $inputtag-obd                               // Regular Converted image
#   │   ├── $inputtag-obd-manifest-cache                // Converted image with cached manifest
#   │   └── $inputtag-obd-extra-layer-cache             // Converted image using the cached layers
#   └── $repository-2
#       ├── $inputtag                                   // Original image
#       ├── $inputtag-obd-manifest-cross-repo-cache     // Converted image with cached manifest + cross repo mount
#       ├── $inputtag-extra-layer-2                     // Original image with one added random extra layer
#       └── $inputtag-obd-layer-cache-cross-repo-mount  // Converted image using the cached layers + cross repo mount

# Validation Examples
registry=$1 # registry to push to
username=$2 # username for registry
password=$3 # password for registry
sourceImage=$4 # public image to convert
repository=$5 # repository to push to
inputtag=$6 # input tag
mysqldbuser=$7 # mysql user
mysqldbpassword=$8 # mysql password

oras login $registry -u $username -p $password
oras cp $sourceImage $registry/$repository:$inputtag

# Manifest Cache Workloads
# Try one conversion
echo "" && echo "___REGULAR__CONVERSION___"
./bin/convertor --repository $registry/$repository -u $username:$password --input-tag $inputtag --oci --overlaybd $inputtag-obd --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql 

echo "" && echo "___CACHED_MANIFEST_CONVERSION___"
# Retry, result manifest should be cached the tag doesnt seem to be getting recreated. Might need a push nonetheless to guarantee the tag?
./bin/convertor --repository $registry/$repository -u $username:$password --input-tag $inputtag --oci --overlaybd $inputtag-obd-manifest-cache --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql

echo "" && echo "___CACHED_MANIFEST_CONVERSION_CROSS_REPO___"
# Retry, cross repo mount
oras cp $sourceImage $registry/$repository-2:$inputtag
./bin/convertor --repository $registry/$repository-2 -u $username:$password --input-tag $inputtag --oci --overlaybd $inputtag-obd-manifest-cross-repo-cache --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql

# Layer Cache Workloads
# Cache from the same repo
echo "" && echo "___CACHED_LAYER_CONVERSION___"
dt=$(date +%s)
echo "FROM $sourceImage" > sample.Dockerfile
data=$(echo "RUN echo \"Random value: $dt-$RANDOM\" > random-file.txt")
echo $data >> sample.Dockerfile
docker build -f sample.Dockerfile -t "$registry/$repository:$inputtag-extra-layer" .
docker push "$registry/$repository:$inputtag-extra-layer"
./bin/convertor --repository $registry/$repository -u $username:$password --input-tag $inputtag-extra-layer --oci --overlaybd $inputtag-obd-extra-layer-cache --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql
rm sample.Dockerfile

# Retry with cross repo cache
echo "" && echo "___CACHED_LAYER_CONVERSION_CROSS_REPO___"
echo "FROM $sourceImage" > sample.Dockerfile
data=$(echo "RUN echo \"Random value: $dt-$RANDOM\" > random-file.txt")
echo "$data" >> sample.Dockerfile
docker build -f sample.Dockerfile -t "$registry/$repository-2:$inputtag-extra-layer-2" .
docker push "$registry/$repository-2:$inputtag-extra-layer-2"
./bin/convertor --repository $registry/$repository-2 -u $username:$password --input-tag $inputtag-extra-layer-2 --oci --overlaybd $inputtag-obd-layer-cache-cross-repo-mount --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql
rm sample.Dockerfile

# Converted descriptors for the original image should match
echo "" && echo "___VALIDATE_CACHED_MANIFEST_CONVERSIONS___"
desc_obd=$(oras manifest fetch $registry/$repository:$inputtag-obd --descriptor | jq -r '.digest')
# desc_obd_manifest_cache=$(oras manifest fetch $registry/$repository:$inputtag-obd-manifest-cache --descriptor)
desc_obd_manifest_cross_repo_cache=$(oras manifest fetch $registry/$repository-2:$inputtag-obd-manifest-cross-repo-cache --descriptor | jq -r '.digest')

if [[ $desc_obd == $desc_obd_manifest_cross_repo_cache ]]; then
    echo "All three images have matching descriptors:"
    echo "$inputtag-obd: $desc_obd"
    echo "$inputtag-obd-manifest-cache: $desc_obd_manifest_cache"
    echo "$inputtag-obd-manifest-cross-repo-cache: $desc_obd_manifest_cross_repo_cache"
else
    echo "Digests do not match for manifest cached images"
    echo "$inputtag-obd: $desc_obd"
    echo "$inputtag-obd-manifest-cache: $desc_obd_manifest_cache"
    echo "$inputtag-obd-manifest-cross-repo-cache: $desc_obd_manifest_cross_repo_cache"
fi
echo "SUCCESS"

echo "" && echo "___VALIDATE_CACHED_LAYER_CONVERSIONS___"
# Converted descriptors for the extra layer images wont match, but all their layers minus the last one should match
mnfst_obd=$(oras manifest fetch $registry/$repository:$inputtag-obd)
mnfst_obd_extra_layer_cache=$(oras manifest fetch $registry/$repository:$inputtag-obd-extra-layer-cache )
mnfst_obd_extra_layer_cache_cross_repo=$(oras manifest fetch $registry/$repository-2:$inputtag-obd-layer-cache-cross-repo-mount)

# Extract layers
layers_obd=$(echo "$mnfst_obd" | jq -r '.layers')
layers_obd_extra_layer_cache=$(echo "$mnfst_obd_extra_layer_cache" | jq -r '.layers')
layers_obd_extra_layer_cache_cross_repo=$(echo "$mnfst_obd_extra_layer_cache_cross_repo" | jq -r '.layers')

# Check that all layers except the last one match, manifest digests should also be different
index=0
for layer_obd_original in $(echo "$layers_obd" | jq -r '.[].digest'); do
    layer_obd_extra_layer_cache=$(echo "$layers_obd_extra_layer_cache" | jq -r ".[$index].digest")
    layer_obd_extra_layer_cache_cross_repo=$(echo "$layers_obd_extra_layer_cache_cross_repo" | jq -r ".[$index].digest")

    if [[ "$layer_obd_original" != "$layer_obd_extra_layer_cache" && "$layer_obd_original" != "$layer_obd_extra_layer_cache_cross_repo" ]]; then
        echo "Layers differ at index $index."
        echo "Original: $layer_obd_original"
        echo "Extra Layer Cache: $layer_obd_extra_layer_cache"
        echo "Extra Layer Cache Cross Repo: $layer_obd_extra_layer_cache_cross_repo"
        exit 1
    fi
    index=$(($index+1))
done
echo "All layers except the last one match for the extra layer cache images"

desc_obd_extra_layer_cache=$(oras manifest fetch $registry/$repository:$inputtag-obd-extra-layer-cache --descriptor | jq -r '.digest')
desc_obd_extra_layer_cache_cross_repo=$(oras manifest fetch $registry/$repository-2:$inputtag-obd-layer-cache-cross-repo-mount --descriptor | jq -r '.digest')

if [[ "$desc_obd" == "$desc_obd_extra_layer_cache" || "$desc_obd" == "$desc_obd_extra_layer_cache_cross_repo" || "$desc_obd_extra_layer_cache" == "$desc_obd_extra_layer_cache_cross_repo" ]]; then
    echo "Extra layer images are somehow identical to the original obd image or to each other"
    echo "Original $desc_obd"
    echo "Extra Layer Cache: $desc_obd_extra_layer_cache"
    echo "Extra Layer Cache Cross Repo: $desc_obd_extra_layer_cache_cross_repo"
    exit 1
fi

echo "Extra layer cache images properly have different manifest digests"
echo "SUCCESS"