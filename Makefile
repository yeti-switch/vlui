BIN := vlui

# The release tag, or "dev" when there is none. Deliberately not --always: that
# falls back to a bare commit hash, which reads like a version without being one.
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all
all: build

## web: build the SPA into web/dist, where go:embed picks it up
.PHONY: web
web:
	cd web && npm ci && npm run build

## build: build the SPA, then bake it into a single binary
.PHONY: build
build: web
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/vlui

## build-go: rebuild only the Go side, reusing the existing web/dist
.PHONY: build-go
build-go:
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/vlui

## static: the binary as it ships — no libc, so it runs in distroless
.PHONY: static
static: web
	CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/vlui

.PHONY: test
test:
	go test ./...

.PHONY: check
check:
	@unformatted=$$(gofmt -l cmd internal web/*.go); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi
	go vet ./...
	go test ./...
	cd web && npm run build

## dev: run the API against config.yml; pair with `make dev-web` for HMR
.PHONY: dev
dev:
	go run ./cmd/vlui -config config.yml -debug

## dev-web: Vite dev server on :5173, proxying /api to the Go process
.PHONY: dev-web
dev-web:
	cd web && npm run dev

.PHONY: clean
clean:
	rm -f $(BIN)
	rm -rf web/dist
	mkdir -p web/dist && touch web/dist/.gitkeep
