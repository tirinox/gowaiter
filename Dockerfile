# syntax=docker/dockerfile:1.7

FROM golang:1.26.8-alpine3.24 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/gowaiter .

FROM alpine:3.24

RUN apk add --no-cache ca-certificates \
    && addgroup -S gowaiter \
    && adduser -S -G gowaiter gowaiter \
    && mkdir -p /data \
    && chown gowaiter:gowaiter /data

WORKDIR /app

COPY --from=build /out/gowaiter /usr/local/bin/gowaiter

ENV TIMER_DB=/data/gowaiter.db

VOLUME ["/data"]

USER gowaiter

EXPOSE 10025

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -T 2 -O /dev/null http://127.0.0.1:10025/readyz || exit 1

ENTRYPOINT ["gowaiter"]
