# used to install binaries
SN_DESTDIR=/opt/overlaybd/snapshotter
SN_CFGDIR=/etc/overlaybd-snapshotter

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
