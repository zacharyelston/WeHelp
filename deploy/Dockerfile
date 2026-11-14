FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/wehelpd ./cmd/wehelp

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/wehelpd /usr/local/bin/wehelpd
EXPOSE 8080
ENTRYPOINT ["wehelpd"]
CMD ["serve"]
