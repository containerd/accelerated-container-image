# used to install binaries
DESTDIR=/usr/local/bin

# command
COMMANDS=overlaybd-snapshotter ctr
BINARIES=$(addprefix bin/,$(COMMANDS))

all: binaries

binaries: $(BINARIES) ## build binaries into bin

# force to rebuild all the binaries
force:

# build a binary from cmd
bin/%: cmd/% force
	@echo "$@"
	@GOOS=linux go build -o $@ ./$<

install: ## install binaries from bin
	@mkdir -p $(DESTDIR)
	@install $(BINARIES) $(DESTDIR)

clean:
	@rm -rf ./bin
