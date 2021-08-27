download_url="https://github.com/goharbor/harbor/releases/download/v2.3.2/harbor-offline-installer-v2.3.2.tgz"

mkdir ~/app
cp src/github.com/alibaba/accelerated-container-image/pkg/p2p/test/harbor/harbor.yml ~/app/harbor.yml
cd ~/app || exit 1
wget -q $download_url -O harbor.tgz
tar xzvf harbor.tgz
cp harbor.yml harbor/harbor.yml
cd harbor || exit 1
sudo ./install.sh