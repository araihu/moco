# syntax=docker/dockerfile:1
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# The allowlist in .dockerignore includes only Go sources and module metadata.
COPY . .
ARG VERSION=dev
ARG REVISION=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/moco-server ./cmd/moco-server && \
    go build -trimpath -ldflags="-s -w" -o /out/moco-backup ./cmd/moco-backup && \
    go build -trimpath -ldflags="-s -w" -o /out/moco-audit-export ./cmd/moco-audit-export && \
    go build -trimpath -ldflags="-s -w" -o /out/moco-ops ./cmd/moco-ops && \
    mkdir -p /runtime/data /runtime/backups /runtime/mirror /runtime/tmp /runtime/run/moco && \
    chmod 0700 /runtime/data /runtime/backups /runtime/mirror /runtime/run/moco && chmod 1777 /runtime/tmp

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Mocó" \
      org.opencontainers.image.source="https://github.com/araihu/moco" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/ /
COPY --from=build --chown=65532:65532 /runtime/ /
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
WORKDIR /data
EXPOSE 8443 9443
STOPSIGNAL SIGTERM
ENTRYPOINT ["/moco-server"]
