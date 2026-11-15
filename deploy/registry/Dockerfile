# WeHelp Registry image (#182). Build from the repository root, or let
# deploy/registry.sh up do it. Pinned to the patch release go.mod names
# (toolchain), like deploy/Dockerfile.
FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/wehelp-registry ./cmd/wehelp-registry
# The key folder, so a new named volume mounted there starts owned by nonroot.
RUN mkdir -p /out/keys && chmod 700 /out/keys

# Runs as the distroless nonroot user (uid 65532). The signing key lives on
# the registrykeys volume at /var/lib/wehelp-registry (keygen creates it).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wehelp-registry /usr/local/bin/wehelp-registry
COPY --from=build --chown=65532:65532 /out/keys /var/lib/wehelp-registry
ENV WEHELP_REGISTRY_SIGNING_KEY=/var/lib/wehelp-registry/signing.key
EXPOSE 8090
ENTRYPOINT ["wehelp-registry"]
CMD ["serve"]
