FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bezalel ./cmd/bezalel

FROM alpine:3.20

RUN apk add --no-cache bash coreutils git ripgrep

COPY --from=builder /bezalel /usr/local/bin/bezalel

WORKDIR /workspace
EXPOSE 8080

ENTRYPOINT ["bezalel"]
CMD ["--port", "8080"]
