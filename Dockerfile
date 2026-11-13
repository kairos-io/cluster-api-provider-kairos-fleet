# syntax=docker/dockerfile:1.28
# Build the manager binary.
#
# golang:1.27-bookworm pinned by digest. The builder must be at least go.mod's
# `go` directive, not equal to it: a newer toolchain builds an older-directive
# module fine, and the directive cannot be raised past what golangci-lint is
# built with (it refuses to load a module targeting a newer Go than itself).
# Bump the digest via renovate/dependabot.
FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /workspace
# Copy the Go module manifests and download deps first so source changes don't
# invalidate the cached module layer.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the go source.
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Reproducible, stripped build. GOARCH comes from buildx (TARGETARCH) so a single
# Dockerfile builds every platform; -trimpath + -buildvcs=false drop local paths and
# VCS stamps for reproducibility; -s -w strip debug info; version/commit are stamped in.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o manager cmd/main.go

# distroless/static:nonroot pinned by digest — minimal, no shell, runs as non-root.
FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
