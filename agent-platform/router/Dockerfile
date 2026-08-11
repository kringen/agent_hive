# Build stage
FROM golang:1.23-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/router ./cmd/router
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/healthcheck ./cmd/healthcheck

# Runtime stage — Alpine instead of distroless: podman's --health-cmd
# always executes via `/bin/sh -c`, even for exec-form HEALTHCHECK
# directives (unlike Docker, which supports true shell-less exec form).
# A fully shell-less image like distroless/static means podman can never
# invoke the healthcheck at all. Alpine keeps the image small (~5MB base)
# while providing the shell podman needs, and our binaries are already
# static (CGO_ENABLED=0) so musl vs glibc doesn't matter.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/router /router
COPY --from=builder /out/healthcheck /healthcheck
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/healthcheck"]
ENTRYPOINT ["/router"]
