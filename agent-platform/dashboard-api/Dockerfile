# Build stage
FROM golang:1.23-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/dashboard-api ./cmd/dashboard-api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/healthcheck ./cmd/healthcheck

# Runtime stage
FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/dashboard-api /dashboard-api
COPY --from=builder /out/healthcheck /healthcheck
VOLUME /data
EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/healthcheck"]
ENTRYPOINT ["/dashboard-api"]
