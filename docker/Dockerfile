# Copyright (c) 2025-2026 Hemi Labs, Inc.
# Use of this source code is governed by the MIT License,
# which can be found in the LICENSE file.

# Build stage
FROM golang:1.24.5-alpine3.22@sha256:daae04ebad0c21149979cd8e9db38f565ecefd8547cf4a591240dc1972cf1399 AS builder

ARG GO_LDFLAGS

WORKDIR /build/

COPY go.mod .
COPY go.sum .
RUN go mod download && go mod verify

COPY . .
RUN GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) CGO_ENABLED=0 GOGC=off \
    go build -trimpath -ldflags="-s -w $GO_LDFLAGS -extldflags '-static'" \
    -o /build/bin/dave ./cmd/dave

# Run stage
FROM cgr.dev/chainguard/wolfi-base@sha256:02dab76bd852a70556b5b2002195c8a5fdab77d323c433bf6642aab080489795

RUN apk add --no-cache rsync

# Build metadata
ARG VERSION
ARG VCS_REF
ARG BUILD_DATE
LABEL maintainer="Hemi Labs" \
      org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.authors="Hemi Labs" \
      org.opencontainers.image.url="https://github.com/hemilabs/dave" \
      org.opencontainers.image.documentation="https://github.com/hemilabs/dave#readme" \
      org.opencontainers.image.source="https://github.com/hemilabs/dave" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="Hemi Labs" \
      org.opencontainers.image.title="Dave" \
      org.opencontainers.image.description="Data backup and archival tool" \
      org.opencontainers.image.base.name="cgr.dev/chainguard/wolfi-base"

# Copy binary
COPY --from=builder /build/bin/dave /usr/local/bin/dave

USER nonroot
WORKDIR /etc/dave/
ENTRYPOINT ["/usr/local/bin/dave"]
