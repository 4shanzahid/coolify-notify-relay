# syntax=docker/dockerfile:1
# Keep in step with go.mod `toolchain` and mise.toml (Go security fixes land in patch releases).
FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /relay ./cmd/relay

# distroless/static ships CA certificates (needed for Chat/GitHub HTTPS) and no shell.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /relay /relay
# Routing lives in the image so changes go through git; override with a Coolify file mount if needed.
COPY config.json /config/config.json
EXPOSE 8080
USER nonroot:nonroot
# Exec form: there is no shell. Coolify detects this and uses it instead of its curl/wget check.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/relay", "healthcheck"]
ENTRYPOINT ["/relay"]
