BINARY := tg-bridge
PKG    := ./cmd/tg-bridge

# Install layout — override on the make command line if you want a different
# prefix, e.g. `make install PREFIX=/usr/local/tg-bridge`.
PREFIX  := /opt/tg-bridge
USER    := tgbridge
GROUP   := tgbridge
UNIT    := tg-bridge.service
SYSTEMD := /etc/systemd/system

.PHONY: build build-arm64 build-armv7 run tidy clean \
        install upgrade uninstall \
        status logs restart start stop

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

# --- systemd install -------------------------------------------------------
# `install` does the whole one-shot setup: build, create the service user if
# missing, drop the binary + unit + config in place, enable, start. Idempotent
# — safe to re-run; that's also what `upgrade` is for after a rebuild.
#
# config.yaml is only seeded from config.example.yaml on first install; later
# runs leave the existing $(PREFIX)/config.yaml untouched.

install: build
	@echo ">> ensuring user '$(USER)' exists"
	@id -u $(USER) >/dev/null 2>&1 || sudo useradd --system --home $(PREFIX) --shell /usr/sbin/nologin $(USER)
	@echo ">> installing binary to $(PREFIX)/$(BINARY)"
	sudo install -d -o $(USER) -g $(GROUP) -m 0750 $(PREFIX)
	sudo install -d -o $(USER) -g $(GROUP) -m 0700 $(PREFIX)/data
	sudo install -o $(USER) -g $(GROUP) -m 0755 bin/$(BINARY) $(PREFIX)/$(BINARY)
	@if [ ! -f $(PREFIX)/config.yaml ]; then \
	  if [ -f config.yaml ]; then \
	    echo ">> seeding $(PREFIX)/config.yaml from ./config.yaml"; \
	    sudo install -o $(USER) -g $(GROUP) -m 0600 config.yaml $(PREFIX)/config.yaml; \
	  else \
	    echo ">> seeding $(PREFIX)/config.yaml from config.example.yaml — edit before relying on it"; \
	    sudo install -o $(USER) -g $(GROUP) -m 0600 config.example.yaml $(PREFIX)/config.yaml; \
	  fi; \
	else \
	  echo ">> keeping existing $(PREFIX)/config.yaml"; \
	fi
	@echo ">> installing systemd unit to $(SYSTEMD)/$(UNIT)"
	sudo install -m 0644 systemd/$(UNIT) $(SYSTEMD)/$(UNIT)
	sudo systemctl daemon-reload
	sudo systemctl enable --now $(UNIT)
	@echo ">> done. tail logs with: make logs"
	@$(MAKE) --no-print-directory status

upgrade: build
	sudo install -o $(USER) -g $(GROUP) -m 0755 bin/$(BINARY) $(PREFIX)/$(BINARY)
	sudo systemctl restart $(UNIT)
	@$(MAKE) --no-print-directory status

uninstall:
	-sudo systemctl disable --now $(UNIT)
	sudo rm -f $(SYSTEMD)/$(UNIT)
	sudo systemctl daemon-reload
	sudo rm -rf $(PREFIX)
	@echo ">> service removed. user '$(USER)' kept (remove with: sudo userdel $(USER))"

status:
	-systemctl status $(UNIT) --no-pager

logs:
	journalctl -u $(UNIT) -f

restart:
	sudo systemctl restart $(UNIT)

start:
	sudo systemctl start $(UNIT)

stop:
	sudo systemctl stop $(UNIT)
