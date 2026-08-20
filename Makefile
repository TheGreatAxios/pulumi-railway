.PHONY: build install schema sdk generate check-generated nodejs-lock nodejs-install nodejs-build package-nodejs example test coverage vet lint assert-version release-snapshot ci clean

PROVIDER := pulumi-resource-railway
ROOT_DIR := $(CURDIR)
BIN_DIR := $(ROOT_DIR)/bin
DIST_DIR := $(ROOT_DIR)/dist
VERSION ?= 0.0.0-dev
VERSION_PACKAGE := github.com/thegreataxios/pulumi-railway/provider/pkg/version
LDFLAGS := -s -w -X $(VERSION_PACKAGE).Version=$(VERSION)
NODEJS_TARBALL := $(DIST_DIR)/thegreataxios-pulumi-railway-$(VERSION).tgz

build:
	mkdir -p "$(BIN_DIR)"
	cd provider && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o "$(BIN_DIR)/$(PROVIDER)" ./cmd/pulumi-resource-railway/

install: build
	mkdir -p "$(HOME)/.pulumi/plugins/resource-railway-v$(VERSION)/"
	cp "$(BIN_DIR)/$(PROVIDER)" "$(HOME)/.pulumi/plugins/resource-railway-v$(VERSION)/$(PROVIDER)"

schema: build
	pulumi package get-schema "$(BIN_DIR)/$(PROVIDER)" | jq 'del(.version)' > schema.json.tmp
	mv schema.json.tmp schema.json

sdk: schema
	@set -e; \
	lock="$(ROOT_DIR)/sdk/nodejs/package-lock.json"; \
	tmp="$(ROOT_DIR)/.nodejs-package-lock.tmp"; \
	if test -f "$$lock"; then cp "$$lock" "$$tmp"; fi; \
	trap 'rm -f "$$tmp"' EXIT; \
	pulumi package gen-sdk --language nodejs --version "$(VERSION)" --out sdk schema.json; \
	if test -f "$$tmp"; then \
		mv "$$tmp" "$$lock"; \
	else \
		cd "$(ROOT_DIR)/sdk/nodejs" && npm install --package-lock-only --ignore-scripts; \
	fi

generate: schema sdk

check-generated: generate
	git diff --exit-code -- schema.json sdk/nodejs

nodejs-lock: schema
	pulumi package gen-sdk --language nodejs --version "$(VERSION)" --out sdk schema.json
	cd sdk/nodejs && npm install --package-lock-only --ignore-scripts

nodejs-install: sdk
	cd sdk/nodejs && npm ci --ignore-scripts

nodejs-build: nodejs-install
	cd sdk/nodejs && npm run build

package-nodejs: nodejs-build
	node scripts/prepare-node-package.mjs "$(VERSION)"
	mkdir -p "$(DIST_DIR)"
	npm pack "$(ROOT_DIR)/sdk/nodejs/bin" --pack-destination "$(DIST_DIR)"
	test -f "$(NODEJS_TARBALL)"

example: nodejs-build
	./sdk/nodejs/node_modules/.bin/tsc --noEmit --project examples/simple/tsconfig.json

test:
	cd provider && go test -race ./...

coverage:
	cd provider && go test -race -coverprofile=coverage.out ./...
	cd provider && go tool cover -func=coverage.out

vet:
	cd provider && go vet ./...

lint:
	cd provider && golangci-lint run ./...

assert-version: build
	test "$$(pulumi package get-schema "$(BIN_DIR)/$(PROVIDER)" | jq -r '.version')" = "$(VERSION)"

release-snapshot:
	goreleaser release --snapshot --clean
	./scripts/verify-release-assets.sh

ci: coverage vet lint check-generated package-nodejs example assert-version

clean:
	rm -rf "$(BIN_DIR)" "$(DIST_DIR)" sdk/nodejs/bin sdk/nodejs/node_modules provider/coverage.out
