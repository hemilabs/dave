# Copyright (c) 2025-2026 Hemi Labs, Inc.
# Use of this source code is governed by the MIT License,
# which can be found in the LICENSE file.

FROM cgr.dev/chainguard/wolfi-base@sha256:02dab76bd852a70556b5b2002195c8a5fdab77d323c433bf6642aab080489795

RUN apk add --no-cache rsync

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/dave /usr/local/bin/dave

USER nonroot
WORKDIR /etc/dave/
ENTRYPOINT ["/usr/local/bin/dave"]
