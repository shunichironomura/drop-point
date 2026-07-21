ARG GO_VERSION=1.26.4

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /out/data \
  && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/droppoint ./cmd/droppoint

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

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
