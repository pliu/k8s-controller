# Base images are pinned by digest so rebuilding a given commit produces the
# same binary, rather than whatever the tag points at that day. Both digests are
# multi-arch indexes, so cross-platform builds still work. They do not update
# themselves: bump them deliberately to pick up Go and base-image security
# fixes, re-resolving with
#   docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'
FROM golang:1.24@sha256:d2d2bc1c84f7e60d7d2438a3836ae7d0c847f4888464e7ec9ba3a1339a1ee804 AS build
WORKDIR /src
# Dependencies change far less often than the source does, so resolving them in
# their own layer keeps an edit to the controller from re-downloading the
# module graph on every build.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# -trimpath keeps the builder's absolute paths out of the binary, so the output
# does not depend on where the build happened to run.
RUN CGO_ENABLED=0 go build -trimpath -o /k8s-controller ./cmd/k8s-controller
FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
COPY --from=build /k8s-controller /k8s-controller
USER nonroot:nonroot
