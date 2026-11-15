# WeHelp Relay image (#180). Build from the repository root:
#   docker compose -f deploy/relay/docker-compose.yml build
# Pinned to the patch release go.mod names (toolchain), like deploy/Dockerfile.
FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/wehelp-relay ./cmd/wehelp-relay

# Runs as the distroless nonroot user (uid 65532). The Relay's folder (keys,
# relay.env, list cache, sites, logs) is mounted at /var/lib/wehelp-relay.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wehelp-relay /usr/local/bin/wehelp-relay
ENV WEHELP_RELAY_DIR=/var/lib/wehelp-relay
ENTRYPOINT ["wehelp-relay"]
CMD ["serve"]
