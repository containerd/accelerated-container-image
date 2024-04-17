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

ARG GO_VERSION
ARG GO_IMAGE=golang:${GO_VERSION}
FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS builder
WORKDIR /src/github.com/containerd/accelerated-container-image
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

FROM scratch AS release
COPY --from=builder /tmp/* /
