# ── Build ──────────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

# CGO is required for mattn/go-sqlite3 (FTS5 + STAT4 + SPELLFIX1 + REGEXP).
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux \
    CGO_CFLAGS="-I$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)" \
    go build \
    -tags "sqlite_fts5 sqlite_stat4" \
    -ldflags="-s -w -extldflags=-static" \
    -o subsarr ./main.go

# ── Runtime ────────────────────────────────────────────────────────────────────
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/subsarr .

VOLUME ["/app/db"]
VOLUME ["/app/storage"]
VOLUME ["/tmp/subscene-archive"]

EXPOSE 8090

ENV SUBSARR_DB_DRIVER=sqlite
ENV SUBSARR_DB_DSN=/app/db/subsarr.db
ENV SUBSARR_STORAGE_BACKEND=filesystem
ENV SUBSARR_STORAGE_PATH=/app/storage
ENV SUBSARR_LISTEN=0.0.0.0:8090

ENTRYPOINT ["/app/subsarr"]
CMD ["serve"]
