# used to install binaries
SN_DESTDIR=/opt/overlaybd/snapshotter

# command
COMMANDS=overlaybd-snapshotter ctr
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
	@GOOS=linux go build -o $@ ./$<

install: ## install binaries from bin
	@mkdir -p ${SN_DESTDIR}
	@install $(BINARIES) $(SN_DESTDIR)
test: ## run tests that require root
	@go test ${GO_TESTFLAGS} ${GO_PACKAGES} -test.root

clean:
	@rm -rf ./bin
