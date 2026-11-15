FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates curl git xz-utils zsh \
    && dpkg --compare-versions "$(git version | cut -d' ' -f3)" ge 2.43.0 \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) arch=x64; checksum=0bd4ec393b46a71f2aa8df541214350f9755b862dfa9771c0315a0abde8046e8 ;; \
      arm64) arch=arm64; checksum=c82f1a0ea14192b854301c9f04f29f020a5c6bca3dcd025f868b7a574b546779 ;; \
      *) exit 1 ;; \
    esac; \
    mkdir /download; \
    curl --fail --location --silent --show-error \
      "https://github.com/jdx/mise/releases/download/v2026.10.12/mise-v2026.10.12-linux-${arch}-musl" -o /download/mise; \
    printf '%s  %s\n' "$checksum" /download/mise | sha256sum -c -; \
    install -m 0755 /download/mise /usr/local/bin/mise

ENV MISE_DATA_DIR=/opt/mise MISE_CONFIG_DIR=/opt/mise/config MISE_CACHE_DIR=/opt/mise/cache \
    MISE_YES=1 MISE_TRUSTED_CONFIG_PATHS=/src PATH="/opt/mise/shims:${PATH}"
WORKDIR /src
COPY . .
WORKDIR /src/tests/e2e
RUN mise install \
    && mise exec -- go mod download \
    && mise exec -- go build -o /usr/local/bin/workdrive ../../cmd/workdrive \
    && mise exec -- go build -o /usr/local/bin/terminal ./terminal \
    && mise exec -- wt --version \
    && mise exec -- sh -c 'ln -s "$(command -v wt)" /usr/local/bin/wt'

ENV HOME=/home/e2e GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 TERM=xterm-256color
ENTRYPOINT ["sleep"]
CMD ["infinity"]
