VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt run clean ext

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/buildyard ./cmd/buildyard

test:
	go test ./...

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

clean:
	rm -rf bin maven-ext/target
