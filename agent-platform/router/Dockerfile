# Build stage
FROM golang:1.23-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/router ./cmd/router
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/healthcheck ./cmd/healthcheck

# Runtime stage — distroless, no shell, no package manager.
# Static binary means this image is tens of MB, not hundreds.
FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/router /router
COPY --from=builder /out/healthcheck /healthcheck
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/healthcheck"]
ENTRYPOINT ["/router"]
