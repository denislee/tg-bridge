BINARY := tg-bridge
PKG    := ./cmd/tg-bridge

.PHONY: build build-arm64 build-armv7 run tidy clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) $(PKG)

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY)-linux-arm64 $(PKG)

build-armv7:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY)-linux-armv7 $(PKG)

run: build
	./bin/$(BINARY) --config config.yaml

tidy:
	go mod tidy

clean:
	rm -rf bin/
