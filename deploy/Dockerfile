# Pinned to the patch release go.mod names (toolchain), so the image gets the
# same standard-library security fixes (#43).
FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# BUILD_TAGS=demo adds the seed command and its fixed demo logins, for demo
# hosts only. A field image is built without it (#161).
ARG BUILD_TAGS=""
RUN CGO_ENABLED=0 go build -trimpath -tags "$BUILD_TAGS" -o /out/wehelpd ./cmd/wehelp

# Runs as the distroless nonroot user (uid 65532), not root (#161).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/wehelpd /usr/local/bin/wehelpd
EXPOSE 8080
ENTRYPOINT ["wehelpd"]
CMD ["serve"]
