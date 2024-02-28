#!/bin/bash
#
# run a HTTPS registry

set -x

mkdir -p /app/dockerd/
echo '{ "hosts": ["unix:///app/dockerd/docker.sock"] }' > /etc/docker/daemon.json
dockerd &>/var/log/dockerd.log &

sleep 3

rm -rf /etc/registry/
mkdir -p /etc/registry/certs/
mkdir -p /etc/registry/config/

# generate server certifications
cat << EOF > /etc/registry/openssl.cnf
[req]
distinguished_name = req_distinguished_name
x509_extensions = v3_req
prompt = no

[req_distinguished_name]
C = CN
ST = Beijing
L = Beijing City
O = Alibaba
CN = localhost

[v3_req]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
EOF

openssl req -new -x509 -newkey rsa:2048 -sha256 -nodes -config /etc/registry/openssl.cnf \
  -days 365 -out /etc/registry/certs/server.crt -keyout /etc/registry/certs/server.key

ls /etc/registry/certs/
cp /etc/registry/certs/server.crt /usr/local/share/ca-certificates/registry.crt
update-ca-certificates

# start registry
cat << EOF > /etc/registry/config/config.yml
version: 0.1
log:
  fields:
    service: registry
storage:
  cache:
    blobdescriptor: inmemory
  filesystem:
    rootdirectory: /var/lib/registry
http:
  addr: :5000
  headers:
    X-Content-Type-Options: [nosniff]
  tls:
    certificate: /certs/server.crt
    key: /certs/server.key
health:
  storagedriver:
    enabled: true
    interval: 10s
    threshold: 3
EOF

docker run -d --restart=always --name registry -p 5000:5000 \
  -v /etc/registry/certs:/certs \
  -v /etc/registry/config:/etc/docker/registry/ \
  registry:2

sleep 5s

docker ps -a
apt-get update && apt-get install -y lsof
lsof -i :5000
curl http://localhost:5000/v2/_catalog
lsof -i :5000
curl https://localhost:5000/v2/_catalog
