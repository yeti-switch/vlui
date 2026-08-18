# The CI job cross-compiles the static binaries first and drops them in
# dist/linux/<arch>/, so there is no build stage here and no Go toolchain in the
# image. Cross-compiling in Go is a build flag; cross-BUILDING a container means
# emulating a whole toolchain under QEMU, which is slow and buys nothing.
#
# Base: distroless static, on Debian 13 (trixie).
#
# Why this one, given the binary is CGO_ENABLED=0 and therefore fully static:
#
#   distroless/static  ~2MB. No shell, no package manager, no libc — nothing to
#                      exploit and nothing to patch, so CVE scans stay quiet. It
#                      does ship ca-certificates, which we need: OIDC discovery
#                      is HTTPS, and so is any VictoriaLogs behind TLS. Without
#                      root CAs both fail with an opaque x509 error. Runs as
#                      non-root out of the box.
#
#   scratch            Smaller still, but no CA certificates and no /etc/passwd.
#                      We would have to copy certs in by hand and invent a uid.
#
#   alpine             Has a shell, which is handy for debugging — but brings
#                      apk, busybox and musl, i.e. a package manager and a libc
#                      to keep patched, for a binary that needs neither.
#
#   debian/ubuntu      ~80MB of userland we do not use, and the largest CVE
#                      surface of the four.
#
# Pinned to the Debian major rather than the floating `distroless/static:nonroot`
# (which silently follows whatever is current): a base OS moving under us between
# two builds of the same tag is exactly the surprise a release pipeline should
# not have. Bumping it is then a deliberate one-line change.
#
# tzdata is deliberately not included: the server never resolves a timezone.
# Log timestamps are formatted in the browser, in the reader's own zone.
FROM gcr.io/distroless/static-debian13:nonroot

# buildx sets TARGETARCH per platform in a multi-arch build. Copying a bare
# `vlui` instead would make the amd64 and arm64 stages look for one file at the
# context root, and only one of them would be right.
ARG TARGETARCH

# Same layout as the .deb — /opt/vlui/{bin,etc} — so a container and a systemd
# host are not two different things to remember.
COPY dist/linux/${TARGETARCH}/vlui /opt/vlui/bin/vlui
COPY packaging/config.docker.yml /opt/vlui/etc/config.yml

# 8080 is the app, 9108 the Prometheus exporter. Separate ports because the
# exporter must not sit behind the app's OIDC middleware or its base path.
EXPOSE 8080 9108
USER nonroot:nonroot

# The config path is CMD rather than part of ENTRYPOINT, so it is exactly what a
# caller replaces when they mean to:
#
#   docker run img                          -> the image's config
#   docker run img -config /custom.yml      -> their config
#   docker run img -version                 -> version, no server
ENTRYPOINT ["/opt/vlui/bin/vlui"]
CMD ["-config", "/opt/vlui/etc/config.yml"]
