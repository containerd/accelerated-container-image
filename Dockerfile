
ARG GO_VERSION=latest
ARG GOLANG_IMAGE=golang:${GO_VERSION}
FROM ${GOLANG_IMAGE} AS golang

FROM ubuntu:22.04 AS builder

WORKDIR /go/src/github.com
ARG TARGETARCH

ENV GOTOOLCHAIN=local
COPY --link --from=golang /usr/local/go/ /usr/local/go/

RUN apt update && apt install -y \
    libcurl4-openssl-dev libssl-dev libaio-dev libnl-3-dev libnl-genl-3-dev libgflags-dev libzstd-dev libext2fs-dev libgtest-dev libtool zlib1g-dev e2fsprogs \
    sudo pkg-config autoconf automake \
    g++ cmake make wget git curl \
    && apt clean

COPY ./overlaybd ./overlaybd
COPY ./accelerated-container-image ./accelerated-container-image

RUN export PATH=$PATH:/usr/local/go/bin && \
    cd overlaybd && rm -rf build && mkdir build && cd build && cmake ../ && make -j && make install && cd ../.. && \
    cd accelerated-container-image && make -j && make install

FROM ubuntu:22.04

COPY --from=builder /opt/overlaybd /opt/overlaybd
COPY --from=builder /etc/overlaybd /etc/overlaybd
COPY --from=builder /etc/overlaybd-snapshotter /etc/overlaybd-snapshotter

RUN apt update && apt install -y \
    libcurl4-openssl-dev libaio-dev \
    && apt clean

ENTRYPOINT ["/opt/overlaybd/snapshotter/convertor"]
