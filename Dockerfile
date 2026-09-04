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
    && adduser -S -G gowaiter gowaiter

WORKDIR /app

COPY --from=build /out/gowaiter /usr/local/bin/gowaiter
COPY cron.json ./cron.json

USER gowaiter

EXPOSE 10025

ENTRYPOINT ["gowaiter"]
