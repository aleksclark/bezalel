# Dockerfile.lsp builds a bezalel image that bundles real language servers
# (gopls + typescript-language-server) for the e2e suite. It is intentionally
# much heavier than the production image (it carries the Go toolchain and a
# Node.js runtime) and is NOT meant for deployment — only for exercising the
# lsp_* tools against genuine language servers.

FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bezalel ./cmd/bezalel
# Build gopls with the same toolchain so it matches the runtime Go version.
RUN go install golang.org/x/tools/gopls@latest

FROM alpine:3.20

# Base tooling (same as the production image) plus a Node.js runtime for the
# TypeScript language server.
RUN apk add --no-cache bash coreutils git ripgrep nodejs npm

# Go toolchain + gopls. gopls shells out to `go`, so both must be present.
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /go/bin/gopls /usr/local/bin/gopls
RUN ln -s /usr/local/go/bin/go /usr/local/bin/go

# TypeScript language server (provides tsserver-backed diagnostics).
RUN npm install -g --no-fund --no-audit typescript typescript-language-server \
    && npm cache clean --force

COPY --from=builder /bezalel /usr/local/bin/bezalel

WORKDIR /workspace
EXPOSE 8080

ENTRYPOINT ["bezalel"]
CMD ["--port", "8080"]
