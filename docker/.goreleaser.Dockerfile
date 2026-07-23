# Copyright (c) 2025-2026 Hemi Labs, Inc.
# Use of this source code is governed by the MIT License,
# which can be found in the LICENSE file.

FROM cgr.dev/chainguard/static@sha256:60582b2ae6074f641094af0f370d4ab241aab271858a66223dcde7eee9f51638

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/dave /usr/local/bin/dave

WORKDIR /etc/dave/
ENTRYPOINT ["/usr/local/bin/dave"]
