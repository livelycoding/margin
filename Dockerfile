# syntax=docker/dockerfile:1
FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/margin ./cmd/margin

# `docker build --target binary --output type=local,dest=bin .`
FROM scratch AS binary
COPY --from=build /out/margin /margin

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libgomp1 libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/margin ./bin/margin
COPY assets/pronunciation/ ./assets/pronunciation/
RUN ./bin/margin setup --app-dir /app --runtime-only \
    && rm /app/runtime/onnxruntime/upstream.tgz \
    && chmod -R a+rX /app/runtime
COPY LICENSE THIRD_PARTY_NOTICES.md ./
COPY LICENSES/ ./LICENSES/
COPY static/ ./static/
RUN groupadd --gid 10001 margin \
    && useradd --uid 10001 --gid margin --no-create-home --home-dir /tmp --shell /usr/sbin/nologin margin \
    && mkdir -p /var/lib/margin /var/cache/margin/models \
    && chown -R margin:margin /var/lib/margin /var/cache/margin \
    && chmod 0700 /var/lib/margin /var/cache/margin /var/cache/margin/models
LABEL org.opencontainers.image.licenses="MIT"
USER 10001:10001
EXPOSE 8765
HEALTHCHECK --interval=15s --timeout=5s --start-period=10m --retries=3 \
    CMD ["/app/bin/margin", "healthcheck"]
ENTRYPOINT ["/app/bin/margin"]
CMD ["serve", "--app-dir", "/app", "--host", "0.0.0.0", "--port", "8765", "--data-dir", "/var/lib/margin", "--model-dir", "/var/cache/margin/models", "--download-models", "--no-desktop-notifications"]
