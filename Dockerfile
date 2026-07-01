ARG GO_VERSION=1.26.4

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /out/data \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/drop-point ./cmd/drop-point

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=nonroot:nonroot /out/drop-point /usr/local/bin/drop-point
COPY --from=build --chown=nonroot:nonroot /out/data/ /var/lib/drop-point/

ENV DROP_POINT_LISTEN_ADDR=:8080 \
    DROP_POINT_DATA_DIR=/var/lib/drop-point

WORKDIR /var/lib/drop-point
VOLUME ["/var/lib/drop-point"]
EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/drop-point"]
CMD ["serve"]
