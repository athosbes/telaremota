# Atalhos de compilação (alternativa ao build.ps1). Requer Go 1.27.4+ e make.
VERSION ?= 0.4.0
LDFLAGS := -s -w -X main.version=$(VERSION)
GO      ?= go

.PHONY: all windows hub test vet clean

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

clean:
	rm -rf dist
