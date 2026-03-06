FROM mcr.microsoft.com/devcontainers/base:ubuntu-22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git jq make \
    openssl \
    swaks \
    netcat-openbsd \
    dnsutils \
    shellcheck \
    && rm -rf /var/lib/apt/lists/*
