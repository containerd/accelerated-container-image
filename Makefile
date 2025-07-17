# used to install binaries
SN_DESTDIR=/opt/overlaybd/snapshotter
SN_CFGDIR=/etc/overlaybd-snapshotter

# versioning
RELEASE_VERSION?=1.3.0-runloop
RELEASE_NUM?=1
OBD_VERSION?=1.0.15
GO_VERSION?=1.23

# command
COMMANDS=overlaybd-snapshotter ctr convertor
BINARIES=$(addprefix bin/,$(COMMANDS))

# go packages
GO_PACKAGES=$(shell go list ${GO_TAGS} ./... | grep -v /vendor/)

all: binaries

binaries: $(BINARIES) ## build binaries into bin

# force to rebuild all the binaries
force:

# build a binary from cmd
bin/%: cmd/% force
	@echo "$@"
	@GOOS=linux CGO_ENABLED=0 go build -ldflags "-X 'main.commitID=$$COMMIT_ID'" -o $@ ./$<

install: ## install binaries from bin
	@mkdir -p $(SN_DESTDIR)
	@install $(BINARIES) $(SN_DESTDIR)
	@install -m 0644 script/overlaybd-snapshotter.service $(SN_DESTDIR)
	@mkdir -p ${SN_CFGDIR}
	@install -m 0644 script/config.json ${SN_CFGDIR}
test: ## run tests that require root
	@go test ${GO_TESTFLAGS} ${GO_PACKAGES} -test.root

clean:
	@rm -rf ./bin
	@rm -f *.deb

deb-amd64: ## build .deb package for amd64
	@echo "Building .deb package for amd64..."
	@mkdir -p /tmp/.buildx-cache
	@DOCKER_BUILDKIT=1 docker buildx build \
		--platform linux/amd64 \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg RELEASE_VERSION=$(RELEASE_VERSION) \
		--build-arg RELEASE_NUM=$(RELEASE_NUM) \
		--cache-from type=local,src=/tmp/.buildx-cache \
		--cache-to type=local,dest=/tmp/.buildx-cache \
		-f ci/build_image/Dockerfile.deb \
		--target deb-only \
		-t aci-builder-amd64 \
		--load .
	@docker run --rm -v $(PWD):/output aci-builder-amd64 \
		sh -c "cp /app/overlaybd-snapshotter_*.deb /output/"

deb-arm64: ## build .deb package for arm64
	@echo "Building .deb package for arm64..."
	@mkdir -p /tmp/.buildx-cache
	@DOCKER_BUILDKIT=1 docker buildx build \
		--platform linux/arm64 \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg RELEASE_VERSION=$(RELEASE_VERSION) \
		--build-arg RELEASE_NUM=$(RELEASE_NUM) \
		--cache-from type=local,src=/tmp/.buildx-cache \
		--cache-to type=local,dest=/tmp/.buildx-cache \
		-f ci/build_image/Dockerfile.deb \
		--target deb-only \
		-t aci-builder-arm64 \
		--load .
	@docker run --rm -v $(PWD):/output aci-builder-arm64 \
		sh -c "cp /app/overlaybd-snapshotter_*.deb /output/"

deb: deb-amd64 deb-arm64 ## build .deb packages for both amd64 and arm64

help: ## show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
