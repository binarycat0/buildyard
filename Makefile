VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Install layout: the binary lives in ~/.buildyard/bin next to the data;
# ~/.local/bin/buildyard is a symlink to it so it is on PATH. No sudo needed.
# BUILDYARD_HOME overrides ~/.buildyard.
BUILDYARD_HOME ?= $(HOME)/.buildyard
BINDIR := $(BUILDYARD_HOME)/bin
LINKDIR ?= $(HOME)/.local/bin

.PHONY: build test vet fmt run clean ext install uninstall docs docs-image docs-serve

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/buildyard ./cmd/buildyard

test:
	go vet ./...
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

run: build
	./bin/buildyard serve

# Rebuilds the Maven extension and copies it where the daemon embeds it from.
# The jar is committed, so plain builds do not need Maven.
ext:
	cd maven-ext && mvn -B -q -ntp clean package
	cp maven-ext/target/buildyard-maven-ext.jar internal/ext/
	cd maven-log && mvn -B -q -ntp clean package
	cp maven-log/maven3/target/maven-log-maven3.jar maven-log/maven4/target/maven-log-maven4.jar maven-log/mvnd1/target/maven-log-mvnd1.jar internal/ext/

# Replaces the binary atomically (copy, then rename over the old one): on
# Apple Silicon, overwriting a binary in place can get it killed by the
# code-signing check. A running service is restarted to pick up the new binary.
install: build
	mkdir -p $(BINDIR) $(LINKDIR)
	cp bin/buildyard $(BINDIR)/.buildyard.new
	chmod 755 $(BINDIR)/.buildyard.new
	mv -f $(BINDIR)/.buildyard.new $(BINDIR)/buildyard
	ln -sfn $(BINDIR)/buildyard $(LINKDIR)/buildyard
	@echo "installed $(BINDIR)/buildyard ($(VERSION)), linked from $(LINKDIR)/buildyard"
	@case ":$$PATH:" in *":$(LINKDIR):"*) ;; \
	  *) echo "note: $(LINKDIR) is not on your PATH; add this to ~/.zshrc:"; \
	     echo '  export PATH="$$HOME/.local/bin:$$PATH"';; esac
	@if $(BINDIR)/buildyard service status 2>/dev/null | grep -qE ': (loaded|active)'; then \
	  $(BINDIR)/buildyard service restart; \
	else \
	  echo "next: buildyard service install && buildyard service start"; \
	fi

# Removes the binary, symlink and service; keeps the data (projects, history, logs).
uninstall:
	-$(BINDIR)/buildyard service uninstall
	rm -f $(LINKDIR)/buildyard $(BINDIR)/buildyard
	@echo "kept $(BUILDYARD_HOME) (config, database, logs); delete it yourself if you want"

clean:
	rm -rf bin maven-ext/target site

# Docs site (MkDocs Material in a container, see utils/mkdocs), published to
# https://tools.binaryc.at/buildyard/ by .github/workflows/docs.yml.
DOCKER ?= docker
DOCS_IMAGE ?= buildyard-docs
ifeq ($(notdir $(DOCKER)),podman)
# Rootless podman: run as your own user so site/ stays yours; :z for SELinux.
DOCS_RUN = $(DOCKER) run --rm --userns=keep-id -v "$(CURDIR)":/docs:z
else
DOCS_RUN = $(DOCKER) run --rm --user $(shell id -u):$(shell id -g) -v "$(CURDIR)":/docs
endif

docs-image:
	$(DOCKER) build -q -t $(DOCS_IMAGE) utils/mkdocs >/dev/null

# Builds site/.
docs: docs-image
	$(DOCS_RUN) $(DOCS_IMAGE) build --strict

# http://127.0.0.1:8000; restart to pick up README.md changes.
docs-serve: docs-image
	$(DOCS_RUN) -it -p 8000:8000 $(DOCS_IMAGE) serve -a 0.0.0.0:8000
