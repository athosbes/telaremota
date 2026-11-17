# Atalhos de compilação (alternativa ao build.ps1). Requer Go 1.27.4+ e make.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GO      ?= go

.PHONY: all windows hub test vet clean release

all: windows hub

windows:
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/telaremota.exe ./cmd/telaremota

hub:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/telaremota-hub-linux-amd64 ./cmd/telaremota-hub
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/telaremota-hub-linux-arm64 ./cmd/telaremota-hub

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

# Pacotes de uma versão em dist/release (ex.: make release TAG=v0.5.0)
release:
	bash scripts/release.sh $(TAG)

clean:
	rm -rf dist
