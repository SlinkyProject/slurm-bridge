# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

################################################################################

FROM --platform=${BUILDPLATFORM} golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.sum ./
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o /workspace ./...

################################################################################

# Ref: https://github.com/GoogleContainerTools/distroless
FROM gcr.io/distroless/static:nonroot AS scheduler
WORKDIR /
COPY --from=builder /workspace/scheduler .
USER 65532:65532
ENTRYPOINT ["/scheduler"]

################################################################################

# Ref: https://github.com/GoogleContainerTools/distroless
FROM gcr.io/distroless/static:nonroot AS controllers
WORKDIR /
COPY --from=builder /workspace/controllers .
USER 65532:65532
ENTRYPOINT ["/controllers"]

################################################################################

# Ref: https://github.com/GoogleContainerTools/distroless
FROM gcr.io/distroless/static:nonroot AS admission
WORKDIR /
COPY --from=builder /workspace/admission .
USER 65532:65532
ENTRYPOINT ["/admission"]
