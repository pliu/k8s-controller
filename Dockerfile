FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /controller ./cmd/controller && CGO_ENABLED=0 go build -o /server ./cmd/server
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /controller /controller
COPY --from=build /server /server
USER nonroot:nonroot
