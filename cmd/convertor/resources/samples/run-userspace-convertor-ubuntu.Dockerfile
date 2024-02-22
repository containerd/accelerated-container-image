#NOTE: This Dockerfile should be executed from the root of the repository
# docker build . -f ./cmd/convertor/resources/samples/run-userspace-convertor-ubuntu.Dockerfile -t convertor

FROM ubuntu:latest AS base
# Required Build/Run Tools Dependencies for Overlaybd tools
RUN apt-get update && \
    apt-get install -y ca-certificates && \
    update-ca-certificates

RUN apt update && \
    apt install -y libcurl4-openssl-dev libext2fs-dev libaio-dev mysql-server

# --- OVERLAYBD TOOLS ---
FROM base As overlaybd-build
RUN apt update && \
    apt install -y libgflags-dev libssl-dev libnl-3-dev libnl-genl-3-dev libzstd-dev && \
    apt install -y zlib1g-dev binutils make git wget sudo tar gcc cmake build-essential g++ && \
    apt install -y uuid-dev libjson-c-dev libkmod-dev libsystemd-dev autoconf automake libtool libpci-dev nasm && \
    apt install -y pkg-config

# Download and install Golang version 1.21
RUN wget https://go.dev/dl/go1.20.12.linux-amd64.tar.gz && \
    tar -C /usr/local -xzf go1.20.12.linux-amd64.tar.gz && \
    rm go1.20.12.linux-amd64.tar.gz

# Set environment variables
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/go"

RUN git clone https://github.com/containerd/overlaybd.git && \
    cd overlaybd && \
    git submodule update --init && \
    mkdir build && \
    cd build && \
    cmake ..  -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=0 -DENABLE_DSA=0 -DENABLE_ISAL=0 && \
    make -j8 && \
    make install

# --- BUILD LOCAL CONVERTER ---
FROM overlaybd-build AS convert-build
WORKDIR /home/limiteduser/accelerated-container-image
COPY . .
WORKDIR /home/limiteduser/accelerated-container-image
RUN make

# --- FINAL ---
FROM base
WORKDIR /home/limiteduser/

# Copy Conversion Tools
COPY --from=overlaybd-build /opt/overlaybd/bin /opt/overlaybd/bin
COPY --from=overlaybd-build /opt/overlaybd/lib /opt/overlaybd/lib
COPY --from=overlaybd-build /opt/overlaybd/baselayers /opt/overlaybd/baselayers

# This is necessary for overlaybd_apply to work
COPY --from=overlaybd-build /etc/overlaybd/overlaybd.json /etc/overlaybd/overlaybd.json
COPY --from=convert-build /home/limiteduser/accelerated-container-image/bin/convertor ./bin/convertor

# Useful resources
COPY cmd/convertor/resources/samples/mysql.conf ./mysql.conf
COPY cmd/convertor/resources/samples/mysql-db-setup.sh ./mysql-db-setup.sh
COPY cmd/convertor/resources/samples/mysql-db-manifest-cache-sample-workload.sh ./mysql-db-manifest-cache-sample-workload.sh
CMD ["./bin/convertor"]