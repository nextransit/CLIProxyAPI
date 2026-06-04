FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN effective_version="${VERSION}"; \
    if [ -n "${BUILD_DATE}" ] && [ "${BUILD_DATE}" != "unknown" ]; then \
      effective_version="${VERSION}-${BUILD_DATE}"; \
    fi; \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${effective_version}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM alpine:3.22.0

# Install CA certificates so the embedded HTTP client can verify TLS chains
# against system roots without having to create per-request OpenSSL contexts
# (which would otherwise leak memory and file descriptors under load).
RUN apk add --no-cache ca-certificates tzdata \
    && update-ca-certificates

RUN mkdir /CLIProxyAPI

# Create a non-root account to run the server. The bind-mounted config,
# assets, auth and log paths on the host stay owned by the host user, so
# the runtime uid/gid are kept configurable via build args.
ARG UID=65532
ARG GID=65532
RUN addgroup -g ${GID} -S cli && adduser -u ${UID} -S cli -G cli

RUN mkdir -p /CLIProxyAPI/.data \
    && chown ${UID}:${GID} /CLIProxyAPI/.data

COPY --from=builder --chown=${UID}:${GID} ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

COPY --chown=${UID}:${GID} config.example.yaml /CLIProxyAPI/config.example.yaml
COPY --chown=${UID}:${GID} assets /CLIProxyAPI/assets

WORKDIR /CLIProxyAPI

USER ${UID}:${GID}

EXPOSE 8317

ENV TZ=Asia/Shanghai

# A lightweight healthcheck that the orchestrator can use to detect a wedged
# process without spawning extra binaries inside the container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8317/v0/management/api-keys >/dev/null 2>&1 || exit 1

CMD ["./CLIProxyAPI"]
