# syntax=docker/dockerfile:1.7
# Build the manager binary.
#
# golang:1.26-bookworm pinned by digest — keep the tag in sync with go.mod's `go`
# directive and bump the digest via renovate/dependabot.
FROM golang:1.27-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS builder
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
FROM gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
