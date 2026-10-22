BINARY  := photothing
BACKUP  ?=
PHOTOS  ?= ./photos
DATA    ?= ./data
ADDR    ?= 127.0.0.1:8080
CONFIG  ?=
# with CONFIG=file the settings come from that configuration file instead of PHOTOS/DATA/ADDR
FLAGS   = $(if $(CONFIG),--config=$(CONFIG),-photos $(PHOTOS) -data $(DATA) -addr $(ADDR))
# The version comes from the latest tag (v1.2.3 -> 1.2.3); between tags it is e.g. 1.2.3-4-g0a1b2c3, and
# without any tag 0.0.0-dev.<hash>. Override with VERSION=1.2.3.
VERSION ?= $(shell packaging/version.sh)
override VERSION := $(patsubst v%,%,$(VERSION))
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)$(if $(REPO_URL), -X main.repoURL=$(REPO_URL))

.DEFAULT_GOAL := build

.PHONY: build version packages run scan backup test race vet fmt check tidy install clean distclean help

build: ## Build the binary
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

version: ## Show the version a build would get
	@echo $(VERSION)

packages: ## Build tarball, .deb and .rpm for this machine into dist/ (needs nfpm; ARCH=amd64|arm64)
	VERSION=$(VERSION) packaging/build.sh $(ARCH)

run: build ## Build and serve (PHOTOS=, DATA=, ADDR= or CONFIG=file can be given)
	./$(BINARY) $(FLAGS) serve

scan: build ## Rescan the photo directory
	./$(BINARY) $(FLAGS) scan

backup: build ## Write a backup archive of the database and key (BACKUP=file to choose the name)
	./$(BINARY) $(if $(CONFIG),--config=$(CONFIG),-data $(DATA)) backup $(BACKUP)

test: ## Run the tests
	go test ./...

race: ## Run the tests with the race detector (slow)
	go test -race -count=1 ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the code
	gofmt -w .

check: ## Fail if code is unformatted, then vet and test
	@test -z "$$(gofmt -l .)" || { echo "unformatted files:"; gofmt -l .; exit 1; }
	$(MAKE) vet test

tidy: ## Tidy go.mod
	go mod tidy

install: ## Install the binary into $(GOBIN) or $(GOPATH)/bin
	CGO_ENABLED=0 go install -trimpath -ldflags '$(LDFLAGS)' .

clean: ## Remove the built binary and dist/
	rm -f $(BINARY)
	rm -rf dist

distclean: clean ## Also remove the data directory (database, key, cache)!
	@echo "This deletes $(DATA) including users and the encryption key."
	@read -p "Type 'yes' to continue: " a && [ "$$a" = "yes" ] && rm -rf $(DATA)

help: ## Show this help
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
