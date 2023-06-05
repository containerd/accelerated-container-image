# Usage: ./generate.sh <registry>
# Prerequisites: oras
# Generates simple hello-world images in ./registry folder
# This script serves as a way to regenerate the images in the registry folder if necessary
# and to document the taken steps to generate the test registry. Add more if new images are needed.

srcTag="linux"
srcRepo="hello-world"
srcImage="docker.io/library/$srcRepo:$srcTag"
srcRegistry="docker.io/library"
registry=$1
destFolder="./registry"

echo "Begin image generation based on src image: $srcImage"

# Docker
oras cp --to-oci-layout $srcImage $destFolder/hello-world:docker-list
# Tag Some submanifests
oras cp --to-oci-layout --platform linux/arm64 $srcRegistry/hello-world:linux $destFolder/hello-world/:arm64
oras cp --to-oci-layout --platform linux/amd64 $srcRegistry/hello-world:linux $destFolder/hello-world/:amd64