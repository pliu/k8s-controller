FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /rbac-controller ./cmd/rbac-controller
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /rbac-controller /rbac-controller
USER nonroot:nonroot
