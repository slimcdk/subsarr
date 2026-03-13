# ── Build ──────────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# CGO is required for mattn/go-sqlite3 (FTS5 + STAT4 + SPELLFIX1 + REGEXP).
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux \
    CGO_CFLAGS="-I$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)" \
    go build \
    -tags "sqlite_fts5 sqlite_stat4 no_default_driver" \
    -ldflags="-s -w -extldflags=-static" \
    -o subsarr ./main.go

# ── Runtime ────────────────────────────────────────────────────────────────────
FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/subsarr .

# PocketBase database — must be persisted between containers.
VOLUME ["/app/pb_data"]

# Mount point for dump archives when running the import command:
#   docker run --rm -v /host/dumps:/dumps subsarr \
#     import-dump --archive /dumps/Subscene\ V2.7z.001
VOLUME ["/dumps"]

EXPOSE 8090

ENTRYPOINT ["/app/subsarr"]
CMD ["serve", "--http=0.0.0.0:8090"]
