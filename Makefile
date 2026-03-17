.PHONY: build run lint test clean docker-build

BINARY := subsarr
IMAGE  := subsarr

SQLITE3_DIR := $(shell go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)

## build: compile the binary
build:
	CGO_ENABLED=1 CGO_CFLAGS="-I$(SQLITE3_DIR)" \
	go build -tags "sqlite_fts5 sqlite_stat4" -ldflags="-s -w" -o $(BINARY) ./main.go

## run: run the server directly
run: build
	./$(BINARY) serve

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## test: run tests
test:
	CGO_ENABLED=1 CGO_CFLAGS="-I$(SQLITE3_DIR)" \
	go test -tags "sqlite_fts5 sqlite_stat4" ./... -v -count=1

## clean: remove build artefacts and data
clean:
	rm -f $(BINARY)
	rm -rf tmp/ data/

## docker-build: build the production Docker image locally
docker-build:
	docker build -t $(IMAGE):latest .
