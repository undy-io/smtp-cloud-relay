# smtp-cloud-relay

`smtp-cloud-relay` accepts SMTP mail from local applications (for example Jira/Confluence), parses MIME content, and relays messages to Azure Communication Services Email API.

## What It Does

- SMTP intake using `github.com/emersion/go-smtp`
- MIME parsing for sender, recipients, subject, text/html, attachments
- Delivery modes:
  - `acs` (default): forwards to ACS Email REST API
  - `ses` (prewired): reserved for future AWS SES v2 support; startup currently fails fast
  - `noop`: accepts/logs only (dev mode)
- Security controls to avoid open relay posture:
  - CIDR allowlist
  - SMTP AUTH PLAIN required
  - STARTTLS and optional SMTPS listener
  - optional TLS-required command policy
- HTTP observability endpoints:
  - `/healthz`
  - `/readyz`
  - `/metrics` (placeholder)

## Configuration

All config values support `_FILE` variants for Kubernetes secret mounts.

### Core

- `DELIVERY_MODE` (`acs`, `ses`, or `noop`, default `acs`)
- `SMTP_LISTEN_ADDR` (default `0.0.0.0:2525`)
- `SMTPS_LISTEN_ADDR` (optional)
- `HTTP_LISTEN_ADDR` (default `0.0.0.0:8080`)

### Delivery Tuning

These settings are provider-agnostic and replace legacy ACS-specific tuning env names.

- `DELIVERY_RETRY_ATTEMPTS` (default `3`)
- `DELIVERY_RETRY_BASE_DELAY_MS` (default `1000`)
- `DELIVERY_HTTP_TIMEOUT_MS` (default `30000`)
- `DELIVERY_HTTP_MAX_IDLE_CONNS` (default `200`)
- `DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST` (default `50`)
- `DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS` (default `90000`)

### SMTP Security

- `SMTP_ALLOWED_CIDRS` (required, comma/space/newline separated CIDRs)
- `SMTP_REQUIRE_AUTH` (must be `true`)
- `SMTP_AUTH_PROVIDER` (`static`)
- `SMTP_AUTH_USERNAME` (required)
- `SMTP_AUTH_PASSWORD` (required)
- `SMTP_STARTTLS_ENABLED` (default `true`)
- `SMTP_REQUIRE_TLS` (default `false`)
- `SMTP_TLS_CERT_FILE` (required when STARTTLS or SMTPS enabled)
- `SMTP_TLS_KEY_FILE` (required when STARTTLS or SMTPS enabled)

### SMTP Performance

- `SMTP_MAX_INFLIGHT_SENDS` (default `200`)

### ACS Delivery

- `ACS_ENDPOINT` (optional if connection string contains `endpoint=`)
- `ACS_CONNECTION_STRING` (required in `acs` mode)
- `ACS_SENDER` (required in `acs` mode)

### SES Delivery (Placeholder)

- `SES_REGION` (required in `ses` mode)
- `SES_SENDER` (required in `ses` mode)
- `SES_ENDPOINT` (optional custom endpoint)
- `SES_CONFIGURATION_SET` (optional)
- `SES_ACCESS_KEY_ID` (optional; must pair with `SES_SECRET_ACCESS_KEY`)
- `SES_SECRET_ACCESS_KEY` (optional; must pair with `SES_ACCESS_KEY_ID`)
- `SES_SESSION_TOKEN` (optional; requires both static key fields)

### ACS TLS / Proxy Trust

- `ACS_TLS_CA_FILE` (optional path to extra CA bundle)
- `ACS_TLS_CA_PEM` (optional inline PEM)

Proxy environment variables are supported for outbound ACS traffic:

- `HTTPS_PROXY`
- `HTTP_PROXY`
- `NO_PROXY`

## Local Dev

Because security defaults are strict, a quick local `noop` run typically sets explicit values:

```bash
export DELIVERY_MODE=noop
export SMTP_ALLOWED_CIDRS=127.0.0.1/32
export SMTP_AUTH_USERNAME=jira
export SMTP_AUTH_PASSWORD=secret
export SMTP_STARTTLS_ENABLED=false
export SMTPS_LISTEN_ADDR=

make run
```

Test with `swaks`:

```bash
swaks --to test@example.com --from dev@example.com --server 127.0.0.1 --port 2525 \
  --auth PLAIN --auth-user jira --auth-password secret
```

## Build and Test

```bash
make build
make test
```

Build a container image for Helm deployment:

```bash
make image IMAGE=ghcr.io/your-org/smtp-cloud-relay:0.1.0
```

## Helm Chart

Chart path:

- `deploy/helm/smtp-cloud-relay`

Quick render/lint workflow:

```bash
helm lint deploy/helm/smtp-cloud-relay
helm template smtp-cloud-relay deploy/helm/smtp-cloud-relay \
  --api-versions cert-manager.io/v1 \
  --set certManager.issuerRef.name=cluster-issuer \
  --set image.repository=ghcr.io/your-org/smtp-cloud-relay \
  --set image.tag=0.1.0 \
  --set certManager.dnsNames[0]=smtp-relay.example.internal
```

The chart includes:

- Deployment / Service / ServiceAccount
- ConfigMap + Secret wiring
- cert-manager `Certificate` (optional)
- NetworkPolicy
- PodDisruptionBudget (optional)

Proxy values for Helm are available under:

- `proxy.httpProxy`
- `proxy.httpsProxy`
- `proxy.noProxy`

Additional cert-manager settings for issuer integrations (including eJBCA-backed issuers):

- `certManager.issuerRef.group` (default `cert-manager.io`)
- `certManager.issuerRef.kind`
- `certManager.issuerRef.name`
- `certManager.altNames[]`
- `certManager.subject.organizations[]`
- `certManager.subject.organizationalUnits[]`
- `certManager.subject.countries[]`
- `certManager.subject.localities[]`
- `certManager.subject.provinces[]`
