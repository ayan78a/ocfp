# Multi-stage build. Runtime stage is `scratch`: exactly one static binary
# plus the CA bundle needed to verify TLS to the opencode.ai upstream.
# No shell — the Docker HEALTHCHECK works because `healthcheck` is a
# subcommand of the entrypoint binary itself (it GETs the server's own
# /healthz on $PORT).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=0.1.0-dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/opencode-free-proxy ./cmd/server

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/opencode-free-proxy /app/opencode-free-proxy
USER 65532:65532
EXPOSE 8090
ENV PORT=8090 \
    OFP_UPSTREAM_BASE=https://opencode.ai
ENTRYPOINT ["/app/opencode-free-proxy"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/app/opencode-free-proxy", "healthcheck"]
