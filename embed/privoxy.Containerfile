# Privoxy 4.2.0 from official binaries.
#
# amd64: privoxy.org release build (GPG-signed by Roland Rosenfeld).
# arm64: compiled from the upstream 4.2.0 source tarball (signed by Fabian Keil),
# because upstream publishes no ARM binary and the Debian arm64 package targets
# a newer libpcre2 than Debian 13 ships.
FROM debian@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

ARG TARGETARCH

LABEL org.opencontainers.image.title="Privoxy" \
      org.opencontainers.image.version="4.2.0" \
      org.opencontainers.image.description="Non-caching filtering web proxy from official binary packages" \
      org.opencontainers.image.licenses="GPL-2.0-or-later" \
      org.opencontainers.image.source="https://www.privoxy.org/"

ENV DEBIAN_FRONTEND=noninteractive \
    PRIVOXY_LISTEN_ADDRESS=0.0.0.0:8118 \
    PRIVOXY_PERMIT_ACCESS=""

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl; \
    printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d; \
    chmod 755 /usr/sbin/policy-rc.d; \
    case "${TARGETARCH}" in \
        amd64) \
            url='https://www.privoxy.org/sf-download-mirror/Debian/4.2.0%20%28stable%29%20trixie/privoxy_4.2.0-1~pp+1_amd64.deb'; \
            sha='b524348d1d74ce6de69d7939236a530dc8f6ebd707ca158139ca2fe6ae1a793f'; \
            curl -fsSL "$url" -o /tmp/privoxy.deb; \
            echo "$sha  /tmp/privoxy.deb" | sha256sum -c -; \
            apt-get install -y --no-install-recommends /tmp/privoxy.deb; \
            rm -f /tmp/privoxy.deb; \
            ;; \
        arm64) \
            url='https://www.privoxy.org/sf-download-mirror/Sources/4.2.0%20%28stable%29/privoxy-4.2.0-stable-src.tar.gz'; \
            sha='6f91267f81f626c416994db89ab62f4d09246eebf4754b81186e13a18ee9028f'; \
            apt-get install -y --no-install-recommends \
                adduser autoconf build-essential pkg-config \
                libpcre2-dev libssl-dev libbrotli-dev libzstd-dev zlib1g-dev \
                libpcre2-8-0 libssl3t64 libbrotli1 libzstd1 zlib1g; \
            id -u privoxy >/dev/null 2>&1 || \
                adduser --quiet --system --home /etc/privoxy --no-create-home \
                    --ingroup nogroup --disabled-password privoxy; \
            curl -fsSL "$url" -o /tmp/privoxy.tar.gz; \
            echo "$sha  /tmp/privoxy.tar.gz" | sha256sum -c -; \
            mkdir -p /tmp/privoxy-src; \
            tar -xzf /tmp/privoxy.tar.gz -C /tmp/privoxy-src --strip-components=1; \
            cd /tmp/privoxy-src; \
            autoheader; \
            autoconf; \
            ./configure \
                --prefix=/usr \
                --sysconfdir=/etc/privoxy \
                --localstatedir=/var \
                --with-user=privoxy \
                --with-group=nogroup \
                --with-openssl \
                --with-brotli \
                --with-zstd; \
            make -j"$(nproc)"; \
            make install; \
            cd /; \
            rm -rf /tmp/privoxy-src /tmp/privoxy.tar.gz; \
            apt-get purge -y --auto-remove \
                autoconf build-essential pkg-config \
                libpcre2-dev libssl-dev libbrotli-dev libzstd-dev zlib1g-dev; \
            ;; \
        *) \
            echo "unsupported architecture: ${TARGETARCH}" >&2; \
            exit 1; \
            ;; \
    esac; \
    rm -f /usr/sbin/policy-rc.d; \
    apt-get purge -y --auto-remove ca-certificates curl; \
    rm -rf /var/lib/apt/lists/*

COPY privoxy-entrypoint.sh /usr/local/bin/privoxy-entrypoint.sh
RUN chmod 755 /usr/local/bin/privoxy-entrypoint.sh

EXPOSE 8118

ENTRYPOINT ["/usr/local/bin/privoxy-entrypoint.sh"]
