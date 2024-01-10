# Usage: ./generate.sh $registry $username $password
# Prerequisites: oras
# Generates simple hello-world images in ./registry folder
# This script serves as a way to regenerate the images in the registry folder if necessary
# and to document the taken steps to generate the test registry. Add more if new images are needed.
# do note that it might be necessary to adjust the consts.go file to make sure tests don't break if
# the source hello-world images are updated.

registry=$1
username=$2
password=$3
srcTag="linux"
srcRepo="hello-world"
srcImage="docker.io/library/$srcRepo:$srcTag"
srcRegistry="docker.io/library"
destFolder="./registry"

echo "Begin image generation based on src image: $srcImage"
oras cp --to-oci-layout $srcImage $destFolder/hello-world:docker-list

# Tag some submanifests
oras cp --to-oci-layout --platform linux/arm64 $srcRegistry/hello-world:linux $destFolder/hello-world/:arm64
oras cp --to-oci-layout --platform linux/amd64 $srcRegistry/hello-world:linux $destFolder/hello-world/:amd64

# Add sample converted manifest
oras login $registry -u $username -p $password
oras cp --from-oci-layout $destFolder/hello-world/:amd64 $registry/hello-world:amd64
cwd=$(pwd)
cd ../../../../
make
sudo bin/convertor --repository $registry/hello-world --input-tag amd64 --oci --overlaybd amd64-converted -u $username:$password
cd $cwd
oras cp --to-oci-layout $registry/hello-world:amd64-converted $destFolder/hello-world/:amd64-converted