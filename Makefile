BINARY := dispatch
PKG    := ./cmd/dispatch
BINDIR := bin
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo 0.1.0)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build install test check vet fmt clean run

all: check build

build:
	@mkdir -p $(BINDIR)
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) $(PKG)
	@echo "built $(BINDIR)/$(BINARY) ($(VERSION))"

install:
	go install -ldflags "$(LDFLAGS)" $(PKG)
	@echo "installed $(BINARY) to $$(go env GOPATH)/bin"

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: vet test

run: build
	./$(BINDIR)/$(BINARY)

clean:
	rm -rf $(BINDIR)
