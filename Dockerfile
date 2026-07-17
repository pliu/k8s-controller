FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /k8s-controller ./cmd/k8s-controller
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /k8s-controller /k8s-controller
USER nonroot:nonroot
