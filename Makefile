# grev tools — Go unix filters on TypeSafe's Jev (System One) models.
#
#   make                 build binaries, man pages and shell completions
#   make install         install into $(DESTDIR)$(PREFIX) (builds what's missing)
#   make test            offline tests (no API key needed)
#   make test-live       live tests with your ~/.grevconfig key, capped by --max-cost
#
# GNU make. Packagers: `make VERSION=1.2.3` then
# `make install DESTDIR=… PREFIX=/usr`; install never rebuilds binaries that
# already exist, so build-time GOFLAGS don't have to be repeated.

GO       ?= go
BIN      ?= bin
PREFIX   ?= /usr/local
DESTDIR  ?=
BINDIR   ?= $(PREFIX)/bin
DATADIR  ?= $(PREFIX)/share
MANDIR   ?= $(DATADIR)/man

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG      := github.com/aurorainfra/grev
# Extra Go linker flags (e.g. -s -w, or -linkmode=external for distro builds).
# Not called LDFLAGS: that is the C linker's variable in most build systems.
GO_LDFLAGS ?=
GOBUILD  := $(GO) build -trimpath -ldflags "$(GO_LDFLAGS) -X $(PKG)/internal/cli.Version=$(VERSION)"

CMDS     := $(sort $(notdir $(wildcard cmd/*)))
STAMP    := $(BIN)/.built

# Reproducible man page dates: the last commit's time unless set.
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null)
ifneq ($(SOURCE_DATE_EPOCH),)
export SOURCE_DATE_EPOCH
endif

.PHONY: all build man completions install uninstall test test-race test-live eval \
	fmt lint clean dist-src $(CMDS)

all: build man completions

# Always rebuilds (Go's cache keeps this cheap).
build:
	$(GOBUILD) -o $(BIN)/ ./cmd/...
	@touch $(STAMP)

# Built only when missing: what man, completions and install depend on.
$(STAMP):
	$(GOBUILD) -o $(BIN)/ ./cmd/...
	@touch $@

$(CMDS):
	$(GOBUILD) -o $(BIN)/$@ ./cmd/$@

# Man pages come from each tool's own help metadata (hidden --help-man).
man: $(STAMP)
	@mkdir -p man
	@set -e; for t in $(CMDS); do \
		$(BIN)/$$t --help-man > man/$$t.1; \
		gzip -n -9 -f man/$$t.1; \
	done
	@$(BIN)/jev --help-man=config > man/grevconfig.5 && gzip -n -9 -f man/grevconfig.5
	@$(BIN)/jev --help-man=tools > man/grev-tools.7 && gzip -n -9 -f man/grev-tools.7
	@echo "man pages: $$(ls man | wc -l) in man/"

# Shell completions, also generated from the tools (hidden --help-completion).
completions: $(STAMP)
	@mkdir -p completions/bash completions/zsh completions/fish
	@set -e; for t in $(CMDS); do \
		$(BIN)/$$t --help-completion=bash > completions/bash/$$t; \
		$(BIN)/$$t --help-completion=zsh  > completions/zsh/_$$t; \
		$(BIN)/$$t --help-completion=fish > completions/fish/$$t.fish; \
	done
	@echo "completions: bash, zsh, fish for $(words $(CMDS)) tools in completions/"

install: $(STAMP) man completions
	install -d $(DESTDIR)$(BINDIR) \
		$(DESTDIR)$(MANDIR)/man1 $(DESTDIR)$(MANDIR)/man5 $(DESTDIR)$(MANDIR)/man7 \
		$(DESTDIR)$(DATADIR)/bash-completion/completions \
		$(DESTDIR)$(DATADIR)/zsh/site-functions \
		$(DESTDIR)$(DATADIR)/fish/vendor_completions.d \
		$(DESTDIR)$(DATADIR)/licenses/grev \
		$(DESTDIR)$(DATADIR)/doc/grev
	install -m 0755 $(addprefix $(BIN)/,$(CMDS)) $(DESTDIR)$(BINDIR)/
	install -m 0644 man/*.1.gz $(DESTDIR)$(MANDIR)/man1/
	install -m 0644 man/*.5.gz $(DESTDIR)$(MANDIR)/man5/
	install -m 0644 man/*.7.gz $(DESTDIR)$(MANDIR)/man7/
	install -m 0644 completions/bash/* $(DESTDIR)$(DATADIR)/bash-completion/completions/
	install -m 0644 completions/zsh/_* $(DESTDIR)$(DATADIR)/zsh/site-functions/
	install -m 0644 completions/fish/*.fish $(DESTDIR)$(DATADIR)/fish/vendor_completions.d/
	install -m 0644 LICENSE-MIT LICENSE-APACHE $(DESTDIR)$(DATADIR)/licenses/grev/
	install -m 0644 README.md $(DESTDIR)$(DATADIR)/doc/grev/

uninstall:
	rm -f $(addprefix $(DESTDIR)$(BINDIR)/,$(CMDS))
	rm -f $(addprefix $(DESTDIR)$(MANDIR)/man1/,$(CMDS:%=%.1.gz))
	rm -f $(DESTDIR)$(MANDIR)/man5/grevconfig.5.gz $(DESTDIR)$(MANDIR)/man7/grev-tools.7.gz
	rm -f $(addprefix $(DESTDIR)$(DATADIR)/bash-completion/completions/,$(CMDS))
	rm -f $(addprefix $(DESTDIR)$(DATADIR)/zsh/site-functions/_,$(CMDS))
	rm -f $(addprefix $(DESTDIR)$(DATADIR)/fish/vendor_completions.d/,$(CMDS:%=%.fish))
	rm -rf $(DESTDIR)$(DATADIR)/licenses/grev $(DESTDIR)$(DATADIR)/doc/grev

# Offline: unit tests plus CLI tests against the fake API server.
test:
	$(GO) vet ./...
	$(GO) test ./...

test-race:
	$(GO) test -race ./internal/...

# Live: real API calls with your key (~/.grevconfig, or TYPESAFE_API_KEY in
# CI), capped with --max-cost. Only the tools ever read the key.
test-live:
	$(GO) test -tags live -count=1 -v ./test/live/...

# Live: labelled fixtures that tune templates, thresholds and packing.
eval:
	$(GO) test -tags eval -count=1 -v -timeout 30m ./eval/...

fmt:
	gofmt -w cmd internal test eval

lint:
	@test -z "$$(gofmt -l cmd internal test eval)" || (gofmt -l cmd internal test eval; exit 1)
	$(GO) vet ./...

# Source tarball of HEAD (committed files only), as release tags provide.
dist-src:
	@mkdir -p dist
	git archive --format=tar.gz --prefix=grev-$(VERSION)/ -o dist/grev-$(VERSION).tar.gz HEAD
	@echo dist/grev-$(VERSION).tar.gz

clean:
	rm -rf $(BIN) man completions dist
