# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO off gives fully static binaries that run on distroless/static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controlplane ./cmd/controlplane \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/racetest ./cmd/racetest

FROM gcr.io/distroless/static-debian12:nonroot AS controlplane
COPY --from=build /out/controlplane /controlplane
EXPOSE 8000
ENTRYPOINT ["/controlplane"]

FROM gcr.io/distroless/static-debian12:nonroot AS worker
COPY --from=build /out/worker /worker
ENTRYPOINT ["/worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS racetest
COPY --from=build /out/racetest /racetest
ENTRYPOINT ["/racetest"]
