# studiolink-client in a container: by default the relay for dial-out mode,
# or any other client command.
#
#   docker build -t studiolink-client .
#   docker run -d --name studiolink-relay --restart unless-stopped \
#     -p 44771:44771 -p 127.0.0.1:44770:44770 \
#     -v studiolink-relay:/data --env-file studiolink.env studiolink-client
#
# See docs/guides/server.md.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X studiolink/internal/version.Version=${VERSION}" \
      -o /out/studiolink-client ./cmd/studiolink-client

# Static binary; distroless adds CA certificates and a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/studiolink-client /usr/local/bin/studiolink-client
# The relay's self-signed certificate lives here; keep it in a volume so its
# fingerprint survives upgrades.
ENV STUDIOLINK_STATE_DIR=/data
WORKDIR /data
VOLUME /data
# 44771: the tunnel the Studio PC dials. 44770: agents (publish it on
# loopback or a private network only).
EXPOSE 44771 44770
ENTRYPOINT ["studiolink-client"]
CMD ["relay", "-listen", "0.0.0.0:44770"]
