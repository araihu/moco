# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:b754301872c53d3dff666ec39e3fae971dc6f5ecd5a9b01fdefabbebb0f59901 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags='-s -w' -o /moco-controller ./cmd/moco-controller
FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Moco management controller" \
      org.opencontainers.image.source="https://github.com/araihu/moco" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /moco-controller /moco-controller
USER 65532:65532
ENTRYPOINT ["/moco-controller"]
