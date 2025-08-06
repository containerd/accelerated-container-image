FROM mcr.microsoft.com/devcontainers/go
# Install additional OS packages
RUN apt-get update && export DEBIAN_FRONTEND=noninteractive \
    && apt-get -y install --no-install-recommends \
        libcurl4-openssl-dev \
        libssl-dev \
        libaio-dev \
        libnl-3-dev \
        libnl-genl-3-dev \
        libgflags-dev \
        libzstd-dev \
        libext2fs-dev \
        libgtest-dev \
        libtool \
        zlib1g-dev \
        e2fsprogs \
        pkg-config \
        autoconf \
        automake \
        g++ \
        cmake \
    && apt-get clean -y \
    && rm -rf /var/lib/apt/lists/*

# Set up workspace directory
WORKDIR /workspaces/accelerated-container-image