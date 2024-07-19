#   Copyright The Accelerated Container Image Authors

#   Licensed under the Apache License, Version 2.0 (the "License");
#   you may not use this file except in compliance with the License.
#   You may obtain a copy of the License at

#       http://www.apache.org/licenses/LICENSE-2.0

#   Unless required by applicable law or agreed to in writing, software
#   distributed under the License is distributed on an "AS IS" BASIS,
#   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#   See the License for the specific language governing permissions and
#   limitations under the License.

# build overlaybd-snapshotter
ARG GO_VERSION
ARG GO_IMAGE=golang:${GO_VERSION}
FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS builder
WORKDIR /src
COPY . .
ENV DEBIAN_FRONTEND=noninteractive
RUN echo 'deb [trusted=yes] https://repo.goreleaser.com/apt/ /' | tee /etc/apt/sources.list.d/goreleaser.list &&  \
    apt update && \
    apt install -y nfpm && \
    go mod tidy

ARG TARGETOS TARGETARCH
ENV GOOS=${TARGETOS}
ENV GOARCH=${TARGETARCH}
ARG RELEASE_VERSION
ENV SEMVER=${RELEASE_VERSION}
ARG RELEASE_NUM
ENV RELEASE=${RELEASE_NUM}
ENV COMMIT_ID="${RELEASE_VERSION}_${RELEASE_NUM}"
RUN make && \
    nfpm pkg --packager deb --target /tmp/ && \
    nfpm pkg --packager rpm --target /tmp/

# build image
FROM ubuntu:22.04 AS release
ARG OBD_VERSION
ARG RELEASE_VERSION
SHELL ["/bin/bash", "-c"]
WORKDIR /app
COPY --from=builder /tmp/overlaybd-snapshotter_${RELEASE_VERSION}_amd64.deb .
COPY ./ci/build_image/start_services.sh .

RUN apt-get update && apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release software-properties-common && \
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg && \
    echo \
      "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu \
      $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null && \
    apt-get update && apt-get install -y docker-ce docker-ce-cli containerd.io && \
    apt-get install -y libnl-3-200 libnl-genl-3-200 libcurl4-openssl-dev libaio-dev wget less kmod && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && \
    wget https://github.com/containerd/overlaybd/releases/download/v${OBD_VERSION}/overlaybd-${OBD_VERSION}-20240717.b5b704b.ubuntu1.22.04.x86_64.deb && \
    dpkg -i overlaybd-${OBD_VERSION}-20240717.b5b704b.ubuntu1.22.04.x86_64.deb && \
    dpkg -i overlaybd-snapshotter_${RELEASE_VERSION}_amd64.deb && \
    sed -i 's/"autoRemoveDev": false,/"autoRemoveDev": true,/g' /etc/overlaybd-snapshotter/config.json && \
    cat /etc/overlaybd-snapshotter/config.json && \
    mkdir -p /etc/containerd/ && \
    echo -e '[proxy_plugins.overlaybd]\n\ttype = "snapshot"\n\taddress = "/run/overlaybd-snapshotter/overlaybd.sock"' | tee -a /etc/containerd/config.toml && \
    cat /etc/containerd/config.toml && \
    chmod +x /app/start_services.sh && \
    cat /app/start_services.sh