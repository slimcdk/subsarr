# The devcontainers Go image, so the `vscode` user, the mounts and the features
# in devcontainer.json all work; this adds what this repo needs on top.
FROM mcr.microsoft.com/devcontainers/go:2-1.26-bookworm

# sqlite3 for looking at a database by hand; zip for building test fixtures.
RUN apt-get update && apt-get install -y --no-install-recommends \
    jq \
    zip \
    sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# The tools the Makefile and the editor settings expect. goose is a library here,
# not a CLI: migrations are embedded in the binary and applied by it, and sqlc is
# gone with the per-dialect generated code it used to produce.
USER vscode
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
USER root

WORKDIR /workspace

# CGO is required for go-sqlite3; the build tags come from the Makefile.
ENV CGO_ENABLED=1
