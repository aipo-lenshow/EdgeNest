# EdgeNest Makefile
#
# Common targets:
#   make web       build the React front-end and sync it into the embed dir
#   make build     build the single binary (embeds the front-end)
#   make run       build and run in standalone mode (dev)
#   make release   cross-compile for linux/amd64 and linux/arm64 + tar.gz
#   make tidy      go mod tidy
#   make clean     remove build artifacts

BINARY      := edgenest
CMD         := ./cmd/edgenest
WEB_DIR     := web
EMBED_DIR   := internal/control/web/dist
VERSION     ?= 1.12.0624
LDFLAGS     := -s -w -X main.version=$(VERSION)
# -trimpath: strip $HOME paths from the binary so different build hosts produce
# identical artifacts (reproducible-build friendly + no env leakage).
GOFLAGS     := -trimpath

.PHONY: web build run release tidy clean fmt test

web:
	cd $(WEB_DIR) && npm ci --prefer-offline --no-audit --no-fund && npm run build
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	cp -r $(WEB_DIR)/dist/* $(EMBED_DIR)/

build:
	CGO_ENABLED=0 GOMEMLIMIT=512MiB go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

run: build
	./bin/$(BINARY) --role standalone

release: web
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 $(CMD)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 $(CMD)
	cd dist && \
	  mkdir -p edgenest-$(VERSION)-linux-amd64 edgenest-$(VERSION)-linux-arm64 && \
	  cp $(BINARY)-linux-amd64 edgenest-$(VERSION)-linux-amd64/$(BINARY) && \
	  cp $(BINARY)-linux-arm64 edgenest-$(VERSION)-linux-arm64/$(BINARY) && \
	  tar -czf edgenest-$(VERSION)-linux-amd64.tar.gz edgenest-$(VERSION)-linux-amd64 && \
	  tar -czf edgenest-$(VERSION)-linux-arm64.tar.gz edgenest-$(VERSION)-linux-arm64 && \
	  sha256sum edgenest-$(VERSION)-linux-*.tar.gz > SHA256SUMS

tidy:
	go mod tidy

fmt:
	go fmt ./...

test:
	go test ./...

clean:
	rm -rf bin dist
