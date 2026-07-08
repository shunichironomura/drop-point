ARG GO_VERSION=1.26.4

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /out/data \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/droppoint ./cmd/droppoint

FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639

COPY --from=build --chown=nonroot:nonroot /out/droppoint /usr/local/bin/droppoint
COPY --from=build --chown=nonroot:nonroot /out/data/ /var/lib/droppoint/

ENV DROPPOINT_LISTEN_ADDR=:8080 \
    DROPPOINT_DATA_DIR=/var/lib/droppoint

WORKDIR /var/lib/droppoint
VOLUME ["/var/lib/droppoint"]
EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/droppoint"]
CMD ["serve"]
