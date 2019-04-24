include .env
BINARY := k8s-spot-rescheduler
VERSION := $(shell git describe --always --dirty --tags 2>/dev/null || echo "undefined")

RED := \033[31m
GREEN := \033[32m
NC := \033[0m

IMG ?= quay.io/pusher/k8s-spot-rescheduler

.NOTPARALLEL:

.PHONY: all
all: test build

.PHONY: build
build: clean $(BINARY)

.PHONY: clean
clean:
	rm -f $(BINARY)

.PHONY: distclean
distclean: clean
	rm -rf vendor
	rm -rf release

.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: vet
vet: vendor
	$(GO) vet ./...

.PHONY: lint
lint:
	@ echo "$(GREEN)Linting code$(NC)"
	$(LINTER) run --disable-all \
		--exclude-use-default=false \
		--enable=govet \
		--enable=ineffassign \
		--enable=deadcode \
		--enable=golint \
		--enable=goconst \
		--enable=gofmt \
		--enable=goimports \
		--skip-dirs=pkg/client/ \
		--deadline=120s \
		--tests ./...
	@ echo

vendor:
	@ echo "$(GREEN)Pulling dependencies$(NC)"
	$(DEP) ensure --vendor-only
	@ echo

.PHONY: test
test: vendor
	@ echo "$(GREEN)Running test suite$(NC)"
	$(GO) test ./...
	@ echo

.PHONY: check
check: fmt lint vet test

.PHONY: build
build: clean $(BINARY)

$(BINARY): fmt vet
	CGO_ENABLED=0 $(GO) build -o $(BINARY) -ldflags="-X main.VERSION=${VERSION}" github.com/pusher/k8s-spot-rescheduler

.PHONY: docker-build
docker-build: check
	docker build --build-arg VERSION=${VERSION}  . -t ${IMG}:${VERSION}
	@echo "$(GREEN)Built $(IMG):$(VERSION)$(NC)"

TAGS ?= latest
.PHONY: docker-tag
docker-tag: docker-build
	@IFS=","; tags=${TAGS}; for tag in $${tags}; do docker tag ${IMG}:${VERSION} ${IMG}:$${tag}; echo "$(GREEN)Tagged $(IMG):$(VERSION) as $${tag}$(NC)"; done

PUSH_TAGS ?= ${VERSION}, latest
.PHONY: docker-push
docker-push: docker-build docker-tag
	@IFS=","; tags=${PUSH_TAGS}; for tag in $${tags}; do docker push ${IMG}:$${tag}; echo "$(GREEN)Pushed $(IMG):$${tag}$(NC)"; done

TAGS ?= latest
.PHONY: docker-clean
docker-clean:
	@IFS=","; tags=${TAGS}; for tag in $${tags}; do docker rmi -f ${IMG}:${VERSION} ${IMG}:$${tag}; done
