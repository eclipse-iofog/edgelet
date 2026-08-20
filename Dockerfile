# syntax=docker/dockerfile:1
# Example:
#   docker buildx build -f Dockerfile \
#     --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=v0.0.0-test \
#     -t ghcr.io/eclipse-iofog/edgelet:test .

FROM golang:1.26.6-trixie@sha256:b75d466dd608587fd66cca705a307ba65b889827d06ad61d6a75f0482b51b7c7 AS builder

ARG BUILDARCH
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_COMMIT=unknown

COPY scripts/install-embed-build-deps /usr/local/bin/install-embed-build-deps
RUN chmod +x /usr/local/bin/install-embed-build-deps \
    && BUILDARCH="${BUILDARCH}" TARGETARCH="${TARGETARCH}" install-embed-build-deps

WORKDIR /src
COPY . .

RUN chmod +x scripts/clean scripts/download scripts/download-root scripts/stage-root-aux \
    scripts/build-crun-static scripts/build-embedded scripts/package-data scripts/build-edgelet \
    scripts/binary_size_check.sh

RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64)   ARCH=amd64 ;; \
      arm64)   ARCH=arm64 ;; \
      arm)     ARCH=arm; export GOARM=7 ;; \
      riscv64) ARCH=riscv64 ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    export VERSION GIT_COMMIT STATIC_BUILD=true; \
    ARCH="${ARCH}" ./scripts/download; \
    ARCH="${ARCH}" ./scripts/build-embedded; \
    ARCH="${ARCH}" ./scripts/build-edgelet fat; \
    ARCH="${ARCH}" ./scripts/package-data; \
    ARCH="${ARCH}" ./scripts/build-edgelet; \
    ARCH="${ARCH}" ./scripts/binary_size_check.sh; \
    test -f "build/edgelet-linux-${ARCH}"; \
    test -f "build/out/data-linux-${ARCH}.tar.zst"

# Unpack embed bundle + minimal rootfs.
FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS base
RUN apk add --no-cache ca-certificates zstd tzdata
ARG TARGETARCH
ARG VERSION=dev
COPY --from=builder /src/build/out/data-linux-${TARGETARCH}.tar.zst /
COPY --from=builder /src/build/edgelet-linux-${TARGETARCH} /thin/edgelet
COPY packaging/edgelet/etc/edgelet/config.default.yaml /config/config.yaml
COPY packaging/edgelet/etc/edgelet/controller-ca.sample.crt /config/cert.crt

RUN set -eux; \
    SOURCE_TAR_ZST="/data-linux-${TARGETARCH}.tar.zst"; \
    if [ ! -f "${SOURCE_TAR_ZST}" ]; then \
      SOURCE_TAR_ZST="/data-linux.tar.zst"; \
    fi; \
    HASH="$(sha256sum "${SOURCE_TAR_ZST}" | awk '{print $1}')"; \
    DATA_ROOT="/image/var/lib/edgelet/data"; \
    BUNDLE="${DATA_ROOT}/${HASH}"; \
    CNI_STABLE="${DATA_ROOT}/cni"; \
    CTD_BIN="/image/var/lib/edgelet-containerd/bin"; \
    CTD_CNI="/image/var/lib/edgelet-containerd/cni/plugins"; \
    CTD_IMG="/image/var/lib/edgelet-containerd/images"; \
    mkdir -p \
      "${BUNDLE}" "${CNI_STABLE}" \
      "${CTD_BIN}" "${CTD_CNI}" "${CTD_IMG}" \
      /image/var/lib/edgelet-containerd/cni/conf \
      /image/etc/cni/net.d \
      /image/run/edgelet /image/var/run /image/var/log/edgelet \
      /image/tmp /image/lib/modules /image/lib/firmware \
      /image/etc/ssl/certs /image/etc/edgelet \
      /image/usr/local/bin /image/bin; \
    zstdcat -d "${SOURCE_TAR_ZST}" | tar -xa -C "${BUNDLE}"; \
    test -x "${BUNDLE}/bin/edgelet"; \
    ln -sfn "${HASH}" "${DATA_ROOT}/current"; \
    CNI_BIN="${BUNDLE}/bin/cni"; \
    for plug in cni bridge host-local loopback portmap; do \
      ln -sf "${CNI_BIN}" "${CNI_STABLE}/${plug}"; \
    done; \
    for plug in $(find "${BUNDLE}/bin" -maxdepth 1 -type l -lname cni -printf '%f\n' 2>/dev/null || true); do \
      ln -sf "${CNI_BIN}" "${CNI_STABLE}/${plug}"; \
    done; \
    cp "${BUNDLE}/bin/containerd-shim-runc-v2" "${CTD_BIN}/"; \
    cp "${BUNDLE}/bin/crun" "${CTD_BIN}/"; \
    chmod 755 "${CTD_BIN}/containerd-shim-runc-v2" "${CTD_BIN}/crun"; \
    for plug in bridge host-local loopback portmap; do \
      ln -sf "${BUNDLE}/bin/${plug}" "${CTD_CNI}/${plug}"; \
    done; \
    if [ -f "${BUNDLE}/images/pause.tar.gz" ]; then \
      cp "${BUNDLE}/images/pause.tar.gz" "${CTD_IMG}/pause.tar.gz"; \
    fi; \
    install -m 755 /thin/edgelet /image/usr/local/bin/edgelet; \
    ln -sf /usr/local/bin/edgelet /image/bin/edgelet; \
    install -m 640 /config/config.yaml /image/etc/edgelet/config.yaml; \
    install -m 644 /config/cert.crt /image/etc/edgelet/cert.crt; \
    cp /etc/ssl/certs/ca-certificates.crt /image/etc/ssl/certs/ca-certificates.crt; \
    echo 'root:x:0:0:root:/:/bin/sh' > /image/etc/passwd; \
    echo 'root:x:0:' > /image/etc/group; \
    echo 'hosts: files dns' > /image/etc/nsswitch.conf; \
    echo "PRETTY_NAME=\"Edgelet ${VERSION}\"" > /image/etc/os-release; \
    chmod 1777 /image/tmp

# collect: COPY only — scratch has no /bin/sh for RUN.
FROM scratch AS collect
ARG VERSION=dev
COPY --from=base /image /
COPY --from=base /usr/share/zoneinfo /usr/share/zoneinfo

FROM scratch

ARG TARGETARCH

COPY --from=collect / /

ENV EDGELET_DAEMON=container
# Set EDGELET_IMAGE to the deployed image ref (e.g. ghcr.io/eclipse-iofog/edgelet:tag) so the
# process-manager watchdog skips removing this container when EDGELET_DAEMON=container.
# cni + aux first; /usr/local/bin before data/current/bin so `edgelet` resolves to thin CLI (not fat).
ENV PATH="/var/lib/edgelet/data/cni:/var/lib/edgelet/data/current/bin/aux:/usr/local/bin:/var/lib/edgelet/data/current/bin:/var/lib/edgelet-containerd/bin:/bin"

VOLUME ["/var/lib/edgelet", "/var/lib/edgelet-containerd", "/etc/edgelet", "/var/log/edgelet"]

LABEL org.opencontainers.image.title="edgelet"
LABEL org.opencontainers.image.description="Edgelet edge runtime (scratch, pre-extracted data bundle)"
LABEL org.opencontainers.image.source="https://github.com/eclipse-iofog/edgelet"
LABEL org.opencontainers.image.vendor="Datasance"
LABEL edgelet.iofog.org/role=edgelet

ENTRYPOINT ["/usr/local/bin/edgelet", "daemon"]
