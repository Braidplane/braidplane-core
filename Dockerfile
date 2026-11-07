# syntax=docker/dockerfile:1

FROM golang:1.26.10-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS go-builder
WORKDIR /src
ENV GOWORK=off
COPY go.mod go.sum ./
RUN --mount=type=cache,id=braidplane-core-go-mod,target=/go/pkg/mod go mod download
COPY . ./
RUN --mount=type=cache,id=braidplane-core-go-mod,target=/go/pkg/mod \
    go mod verify \
    && cd "$(go env GOMODCACHE)/cache/download" \
    && sha256sum --check /src/MPL_SOURCES.sha256 \
    && while read -r checksum archive; do \
        mkdir -p "/out/mpl-sources/$(dirname "$archive")"; \
        cp "$archive" "/out/mpl-sources/$archive" || exit 1; \
    done < /src/MPL_SOURCES.sha256
RUN mkdir -m 1777 /runtime-tmp
RUN --mount=type=cache,id=braidplane-core-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=braidplane-core-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/braidplane-healthcheck ./cmd/healthcheck \
    && CGO_ENABLED=0 go build -trimpath -o /out/braidplane-server . \
    && CGO_ENABLED=0 go build -trimpath -o /out/braidplane-admin ./cmd/admin \
    && CGO_ENABLED=0 go build -trimpath -o /out/braidplane-frontend ./cmd/frontend \
    && CGO_ENABLED=0 go build -trimpath -o /out/braidplane-migrate ./cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -o /out/braidplane ./cli/cmd/braidplane
RUN test "$(dpkg-query -W -f='${Version}' ca-certificates)" = '20250419~deb12u2'
ADD --checksum=sha256:a041820e6feb1effe0598f80e02fb62a40ef312678b85cfbb57d2864d0b5c0b7 \
    https://snapshot.debian.org/file/197e29fabc23db787ce0b5790c82e47db11df66a \
    /out/runtime-sources/ca-certificates_20250419~deb12u2.dsc
ADD --checksum=sha256:934942a985b2efd7a8c122cb1d42cd87911cf6ac365c545938619fcf268ea8f4 \
    https://snapshot.debian.org/file/fde2c52fcb2eca9b91939d726dcce3cc6b6def90 \
    /out/runtime-sources/ca-certificates_20250419~deb12u2.tar.xz

FROM node:24.19.0-bookworm-slim@sha256:a9f5f7c91a432850b2a8a7797adf5eadb6c733ceed61167806cee7ea7fbc29df AS styles
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
# Match ci/toolchain/versions.env instead of relying on the image's npm version.
RUN npm install --global npm@11.17.0
RUN --mount=type=cache,id=braidplane-core-npm,target=/root/.npm npm ci
COPY frontend ./
RUN npm run build:css

FROM scratch AS release-evidence
COPY --from=go-builder /out/ /out/
COPY --from=styles /src/frontend/assets/ /frontend-assets/

FROM debian:bookworm-20260918-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 AS backend
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates git openssh-client tzdata \
    && test "$(dpkg-query -W -f='${Version}' ca-certificates)" = '20250419~deb12u2' \
    && rm -rf /var/lib/apt/lists/* \
    && echo 'braidplane:x:65532:65532:Braidplane runtime:/tmp:/usr/sbin/nologin' >> /etc/passwd \
    && echo 'braidplane:x:65532:' >> /etc/group
WORKDIR /app
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.txt MPL_SOURCES.sha256 SOURCE_AVAILABILITY.txt /usr/share/licenses/braidplane/
COPY --from=go-builder /out/mpl-sources /usr/share/licenses/braidplane/sources
COPY --from=go-builder /out/runtime-sources /usr/share/licenses/braidplane/sources/runtime
COPY --from=go-builder /out/braidplane-healthcheck /braidplane-healthcheck
COPY --from=go-builder /out/braidplane-server /braidplane-server
COPY --from=go-builder /out/braidplane-admin /braidplane-admin
COPY config.toml /etc/braidplane/config.toml
ENV CONFIG_PATH=/etc/braidplane/config.toml HOME=/tmp
USER 65532:65532
EXPOSE 3001
ENTRYPOINT ["/braidplane-server"]
CMD ["api"]

FROM scratch AS frontend
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.txt MPL_SOURCES.sha256 SOURCE_AVAILABILITY.txt /usr/share/licenses/braidplane/
COPY --from=go-builder /out/mpl-sources /usr/share/licenses/braidplane/sources
COPY --from=go-builder /out/runtime-sources /usr/share/licenses/braidplane/sources/runtime
COPY --from=go-builder /usr/share/doc/ca-certificates/copyright /usr/share/licenses/braidplane/runtime/ca-certificates.copyright
COPY --from=go-builder /usr/share/doc/tzdata/copyright /usr/share/licenses/braidplane/runtime/tzdata.copyright
COPY --from=go-builder /usr/share/common-licenses/GPL-2 /usr/share/licenses/braidplane/runtime/GPL-2
COPY --from=go-builder --chmod=1777 /runtime-tmp /tmp
WORKDIR /app
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=go-builder /out/braidplane-healthcheck /braidplane-healthcheck
COPY --from=go-builder /out/braidplane-frontend /braidplane-frontend
COPY --from=styles /src/frontend/assets ./frontend/assets
COPY brand/icons ./brand/icons
COPY config.toml /etc/braidplane/config.toml
ENV CONFIG_PATH=/etc/braidplane/config.toml
USER 65532:65532
EXPOSE 3002
ENTRYPOINT ["/braidplane-frontend"]

FROM scratch AS migrate
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.txt MPL_SOURCES.sha256 SOURCE_AVAILABILITY.txt /usr/share/licenses/braidplane/
COPY --from=go-builder /out/mpl-sources /usr/share/licenses/braidplane/sources
COPY --from=go-builder /out/runtime-sources /usr/share/licenses/braidplane/sources/runtime
COPY --from=go-builder /usr/share/doc/ca-certificates/copyright /usr/share/licenses/braidplane/runtime/ca-certificates.copyright
COPY --from=go-builder /usr/share/doc/tzdata/copyright /usr/share/licenses/braidplane/runtime/tzdata.copyright
COPY --from=go-builder /usr/share/common-licenses/GPL-2 /usr/share/licenses/braidplane/runtime/GPL-2
COPY --from=go-builder --chmod=1777 /runtime-tmp /tmp
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=go-builder /out/braidplane-migrate /braidplane-migrate
COPY postgres/migrations /postgres/migrations
USER 65532:65532
ENTRYPOINT ["/braidplane-migrate"]
CMD ["up"]

FROM scratch AS cli
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.txt MPL_SOURCES.sha256 SOURCE_AVAILABILITY.txt /usr/share/licenses/braidplane/
COPY --from=go-builder /out/mpl-sources /usr/share/licenses/braidplane/sources
COPY --from=go-builder /out/runtime-sources /usr/share/licenses/braidplane/sources/runtime
COPY --from=go-builder /usr/share/doc/ca-certificates/copyright /usr/share/licenses/braidplane/runtime/ca-certificates.copyright
COPY --from=go-builder /usr/share/doc/tzdata/copyright /usr/share/licenses/braidplane/runtime/tzdata.copyright
COPY --from=go-builder /usr/share/common-licenses/GPL-2 /usr/share/licenses/braidplane/runtime/GPL-2
COPY --from=go-builder --chmod=1777 /runtime-tmp /tmp
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=go-builder /out/braidplane /braidplane
ENV HOME=/tmp
USER 65532:65532
ENTRYPOINT ["/braidplane"]
