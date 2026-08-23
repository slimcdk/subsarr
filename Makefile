.PHONY: build run lint test test-dialects test-services test-services-down clean docker-build

BINARY  := subsarr
IMAGE   := subsarr
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# CGO is required for mattn/go-sqlite3; the tags compile in FTS5 (the title
# index) and STAT4 (the statistics the planner needs to pick the right index).
GO_TAGS   := sqlite_fts5 sqlite_stat4
GO_BUILD  := CGO_ENABLED=1 go build -tags "$(GO_TAGS)" -ldflags="-s -w -X github.com/slimcdk/subsarr/internal/version.Version=$(VERSION)"

# Test DSNs for the dialect tests. Without them the suite runs on SQLite alone.
TEST_POSTGRES_DSN := postgres://subsarr:subsarr@localhost:5432/subsarr?sslmode=disable
TEST_MYSQL_DSN    := root:root@tcp(localhost:3306)/subsarr

## build: compile the binary
build:
	$(GO_BUILD) -o $(BINARY) .

## run: run the server directly
run: build
	./$(BINARY) serve

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## test: run the test suite (SQLite only)
test:
	CGO_ENABLED=1 go test -tags "$(GO_TAGS)" -race ./... -count=1

## test-dialects: run the suite against PostgreSQL and MariaDB as well
test-dialects: test-services
	CGO_ENABLED=1 \
	SUBSARR_TEST_POSTGRES_DSN="$(TEST_POSTGRES_DSN)" \
	SUBSARR_TEST_MYSQL_DSN="$(TEST_MYSQL_DSN)" \
	go test -tags "$(GO_TAGS)" -race ./... -count=1

## test-services: start the databases the dialect tests need
test-services:
	docker compose -f docker-compose.test.yaml up -d --wait postgres mariadb

## test-services-down: stop them again
test-services-down:
	docker compose -f docker-compose.test.yaml down -v

## clean: remove build artefacts
##
## Never touches data/ or storage/: a habit must not be able to destroy an
## import that took twelve hours.
clean:
	rm -f $(BINARY)
	rm -rf tmp/

## docker-build: build the production Docker image locally
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):latest .
