FROM mcr.microsoft.com/devcontainers/base:ubuntu-22.04

RUN export DEBIAN_FRONTEND=noninteractive \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
      apt-transport-https \
      ca-certificates \
      curl \
      dnsutils \
      git \
      gnupg \
      jq \
      make \
      netcat-openbsd \
      openssl \
      shellcheck \
      swaks \
    && curl -fsSL https://packages.buildkite.com/helm-linux/helm-debian/gpgkey | gpg --dearmor -o /usr/share/keyrings/helm.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/helm.gpg] https://packages.buildkite.com/helm-linux/helm-debian/any/ any main" > /etc/apt/sources.list.d/helm-stable-debian.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends helm \
    && rm -rf /var/lib/apt/lists/*
