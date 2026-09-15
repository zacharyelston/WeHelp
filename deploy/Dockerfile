FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/wehelp ./cmd/wehelp

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/wehelp /wehelp
EXPOSE 8080
ENTRYPOINT ["/wehelp"]
CMD ["serve"]
