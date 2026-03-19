# smtp-cloud-relay

`smtp-cloud-relay` accepts SMTP mail from local applications such as Jira or Confluence, enforces relay and sender policy, durably spools accepted mail, and delivers it to Azure Communication Services Email or AWS SES v2 from a background worker.

## What It Does

- Accepts SMTP mail over plain SMTP, STARTTLS, or SMTPS
- Requires SMTP auth and CIDR allowlisting to avoid open-relay behavior
- Parses MIME content into a normalized internal message model
- Enforces deterministic sender handling before acceptance
- Durably enqueues accepted mail into a local spool
- Delivers queued mail from a background worker using:
  - `acs` (default): Azure Communication Services Email
  - `ses`: AWS SES v2
  - `noop`: accept and log only
- Exposes:
  - `/healthz`
  - `/readyz`
  - `/metrics`

Runtime notes:

- The embedded SMTP server is single-use per process instance.
- `Shutdown(ctx)` is the bounded shutdown API.
- `Close()` remains a blocking compatibility wrapper.

## Delivery Semantics

The relay no longer submits mail to providers on the SMTP request path.

- SMTP `250` means the message was durably enqueued into the local spool.
- Provider submission happens later in the background worker.
- The relay guarantees no false success for a single SMTP transaction.
- Repeated SMTP submissions across separate transactions are not deduplicated in this phase.
- `SMTP_MAX_INFLIGHT_SENDS` limits concurrent SMTP-path durable enqueue work, not background provider delivery.

Provider behavior:

- ACS uses a submit-plus-poll flow.
- SES is treated as immediate submission completion in the current implementation.

## Configuration

All config values support `_FILE` variants for Kubernetes secret mounts.

### Core

- `DELIVERY_MODE` (`acs`, `ses`, or `noop`, default `acs`)
- `SMTP_LISTEN_ADDR` (default `0.0.0.0:2525`)
- `SMTPS_LISTEN_ADDR` (optional)
- `HTTP_LISTEN_ADDR` (default `0.0.0.0:8080`)

### SMTP Security

- `SMTP_ALLOWED_CIDRS` (required, comma/space/newline-separated CIDRs)
- `SMTP_REQUIRE_AUTH` (must be `true`)
- `SMTP_AUTH_PROVIDER` (`static`)
- `SMTP_AUTH_USERNAME` (required)
- `SMTP_AUTH_PASSWORD` (required)
- `SMTP_STARTTLS_ENABLED` (default `true`)
- `SMTP_REQUIRE_TLS` (default `false`)
- `SMTP_TLS_CERT_FILE` (required when STARTTLS or SMTPS is enabled)
- `SMTP_TLS_KEY_FILE` (required when STARTTLS or SMTPS is enabled)

### SMTP Concurrency

- `SMTP_MAX_INFLIGHT_SENDS` (default `200`)

This is the SMTP enqueue-path concurrency limit. It does not control background worker throughput.

### Sender Policy

- `SENDER_POLICY_MODE` (`rewrite` or `strict`, default `rewrite`)
- `SENDER_ALLOWED_DOMAINS` (optional comma/space/newline-separated sender-domain matchers)

Matcher forms:

- bare entry: exact domain match, case-insensitive
- `glob:`: one-label subdomain wildcard only
- `re:`: full-domain regex, compiled case-insensitively

Examples:

- exact domain: `example.com`
- one-label subdomain wildcard: `glob:*.example.com`
- regex: `re:(?:.+\\.)?example\\.com`

Matching is against the sender domain only, never the full email address.

Outbound provider payloads also include sender trace headers when values are available:

- `X-SMTP-Relay-Envelope-From`
- `X-SMTP-Relay-Header-From`

### Delivery Tuning

These settings are provider-agnostic.

- `DELIVERY_RETRY_ATTEMPTS` (default `3`)
- `DELIVERY_RETRY_BASE_DELAY_MS` (default `1000`)
- `DELIVERY_HTTP_TIMEOUT_MS` (default `30000`)
- `DELIVERY_HTTP_MAX_IDLE_CONNS` (default `200`)
- `DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST` (default `50`)
- `DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS` (default `90000`)

### Spool

- `SPOOL_DIR` (default `/var/lib/smtp-cloud-relay/spool`)
- `SPOOL_POLL_INTERVAL_MS` (default `1000`)

Spool root layout:

- `spool.db`
- `staging/`
- `payloads/`
- `payload-orphans/`

The background worker uses the spool as the durable handoff boundary. Accepted SMTP mail is written here before `250` is returned.

### ACS Delivery

- `ACS_ENDPOINT` (optional if the connection string contains `endpoint=`)
- `ACS_CONNECTION_STRING` (required in `acs` mode)
- `ACS_SENDER` (required in `acs` mode)

### SES Delivery

- `SES_REGION` (required in `ses` mode)
- `SES_SENDER` (required in `ses` mode)
- `SES_ENDPOINT` (optional custom endpoint; must use `https://` when set)
- `SES_CONFIGURATION_SET` (optional)
- `SES_ACCESS_KEY_ID` (optional; must pair with `SES_SECRET_ACCESS_KEY`)
- `SES_SECRET_ACCESS_KEY` (optional; must pair with `SES_ACCESS_KEY_ID`)
- `SES_SESSION_TOKEN` (optional; requires both static key fields)

### Outbound TLS / Proxy Trust

- `OUTBOUND_TLS_CA_FILE` (optional path to an extra CA bundle; applies to ACS and SES)
- `OUTBOUND_TLS_CA_PEM` (optional inline PEM; applies to ACS and SES)

Compatibility aliases still supported:

- `ACS_TLS_CA_FILE`
- `ACS_TLS_CA_PEM`

Proxy environment variables are supported for outbound provider traffic:

- `HTTPS_PROXY`
- `HTTP_PROXY`
- `NO_PROXY`

## Observability

- `/healthz` is unconditional liveness.
- `/readyz` is ready only when:
  - startup recovery has completed
  - SMTP listeners are bound
- `/metrics` exposes real Prometheus metrics for:
  - denied SMTP sessions
  - failed SMTP auth
  - durable enqueue success and failure
  - spool depth by state
  - provider submit outcomes
  - provider poll outcomes
  - retry and reschedule counters

## Local Dev

Because security defaults are strict, a quick local `noop` run typically sets explicit values:

```bash
export DELIVERY_MODE=noop
export SMTP_ALLOWED_CIDRS=127.0.0.1/32
export SMTP_AUTH_USERNAME=jira
export SMTP_AUTH_PASSWORD=secret
export SMTP_STARTTLS_ENABLED=false
export SMTPS_LISTEN_ADDR=
export SENDER_POLICY_MODE=rewrite

make run
```

Test with `swaks`:

```bash
swaks --to test@example.com --from dev@example.com --server 127.0.0.1 --port 2525 \
  --auth PLAIN --auth-user jira --auth-password secret
```

## Build and Test

Current `Makefile` targets:

- `make run` -> `go run ./cmd/relay`
- `make build` -> `go build ./...`
- `make test` -> `go test ./...`
- `make image IMAGE=...` -> build the container image with `docker`, `buildah`, or `podman`
- `make helm-lint` -> lint the Helm chart
- `make helm-template ...` -> render the Helm chart locally
- `make helm-package ...` -> package the Helm chart into `dist/charts/`

Examples:

```bash
make build
make test
```

Build a container image:

```bash
make image IMAGE=ghcr.io/your-org/smtp-cloud-relay:0.1.0
```

Builder selection:

- `make image` prefers `docker`, then falls back to `buildah`, then `podman`
- the checked-in devcontainer is for development and testing, not for container image builds
- run `make image` from a host environment with `docker`, `buildah`, or `podman`, or in CI
- `buildah` uses rootless `vfs` storage and `chroot` isolation as a fallback, but some containerized environments still need extra user-namespace support for it to succeed
- override explicitly with `IMAGE_BUILDER=docker`, `IMAGE_BUILDER=buildah`, or `IMAGE_BUILDER=podman`

Lint and validate the Helm chart:

```bash
make helm-lint
make helm-template \
  IMAGE_REPOSITORY=ghcr.io/undy-io/smtp-cloud-relay \
  CERT_MANAGER_ISSUER_NAME=cluster-issuer \
  CERT_MANAGER_DNS_NAME=smtp-relay.example.internal
bash ./scripts/ci/check-helm.sh
```

Package the Helm chart with explicit release metadata:

```bash
make helm-package CHART_VERSION=0.1.0 CHART_APP_VERSION=0.1.0
```

## Helm Chart

Chart path:

- `deploy/helm/smtp-cloud-relay`

The chart is secure-first and fail-fast:

- `deliveryMode` defaults to `acs`
- TLS defaults to `tls.mode=certManager`
- if STARTTLS, SMTPS, or `smtp.requireTLS` implies TLS, Helm fails render/install unless a valid certificate source is configured
- plaintext mode is only allowed when it is configured explicitly with `tls.mode=disabled`, `smtp.starttlsEnabled=false`, `smtp.smtps.enabled=false`, and `smtp.requireTLS=false`
- the chart intentionally rejects insecure or obviously broken startup settings before a pod is created
- provider secrets and SMTP auth secrets still must be overridden for real deployments
- `image.tag` is optional; when unset it defaults to the chart `appVersion`

Quick render/lint workflow:

```bash
make helm-lint
make helm-template \
  IMAGE_REPOSITORY=ghcr.io/your-org/smtp-cloud-relay \
  IMAGE_TAG=0.1.0 \
  CERT_MANAGER_ISSUER_NAME=cluster-issuer \
  CERT_MANAGER_DNS_NAME=smtp-relay.example.internal
```

`make helm-lint` uses a portable `tls.mode=existingSecret` placeholder because `helm lint` cannot advertise cert-manager API availability. `make helm-template` and CI exercise the default `tls.mode=certManager` render path.

Important pre-release chart values:

- `smtp.listenHost`
- `smtp.port`
- `smtp.smtps.enabled`
- `smtp.smtps.listenHost`
- `smtp.smtps.port`
- `http.listenHost`
- `http.port`
- `tls.mode` (`certManager`, `existingSecret`, `disabled`)
- `tls.existingSecretName` (required when `tls.mode=existingSecret`)
- `networkPolicy.egressTCPPorts`

## GitHub Actions Artifacts

GitHub Actions publishes deployable artifacts to GHCR:

- container image: `ghcr.io/undy-io/smtp-cloud-relay`
- Helm chart: `oci://ghcr.io/undy-io/charts/smtp-cloud-relay`

Stable releases come from semver tags:

- pushing `v0.1.0` requires `deploy/helm/smtp-cloud-relay/Chart.yaml` `version: 0.1.0`
- the publish workflow pushes image tags `0.1.0` and `sha-<shortsha>`
- the publish workflow pushes chart version `0.1.0` with `appVersion: 0.1.0`

Preview artifacts come from `main`:

- image tags: `main` and `sha-<shortsha>`
- chart version: `<Chart.yaml version>-main.<run_number>`
- chart `appVersion`: `sha-<shortsha>`

After each stable release, bump `deploy/helm/smtp-cloud-relay/Chart.yaml` on `main` to the next intended release version before allowing more preview publishes.

Use Helm OCI to pull or install the chart:

```bash
helm pull oci://ghcr.io/undy-io/charts/smtp-cloud-relay --version 0.1.0
helm install smtp-cloud-relay oci://ghcr.io/undy-io/charts/smtp-cloud-relay --version 0.1.0
```

### Spool Persistence

Relevant chart values:

- `spool.dir` (default `/var/lib/smtp-cloud-relay/spool`)
- `spool.pollIntervalMS` (default `1000`)
- `spool.persistence.enabled` (default `true`)
- `spool.persistence.size` (default `10Gi`)
- `spool.persistence.storageClass` (default empty, cluster default)
- `spool.persistence.existingClaim` (default empty)

Deployment contract:

- `acs` and `ses` require durable spool persistence.
- `noop` may use `emptyDir`.
- provider-backed modes are validated as single replica only.
- the chart uses fixed PVC access mode `ReadWriteOnce`.
- the SQLite-backed spool expects single-writer block storage; do not treat RWX/NFS-style shared storage as equivalent.

### TLS, Proxy, And Other Chart Surface

The chart includes:

- Deployment / Service / ServiceAccount
- ConfigMap + Secret wiring
- spool PVC support
- cert-manager `Certificate` (optional)
- NetworkPolicy
- PodDisruptionBudget (optional)

Proxy values:

- `proxy.httpProxy`
- `proxy.httpsProxy`
- `proxy.noProxy`

Listener and TLS values:

- `smtp.listenHost`
- `smtp.port`
- `smtp.smtps.enabled`
- `smtp.smtps.listenHost`
- `smtp.smtps.port`
- `http.listenHost`
- `http.port`
- `tls.mode`
- `tls.existingSecretName`

TLS modes:

- `tls.mode=certManager` renders a `Certificate` and stores the result in `<release fullname>-tls`
- `tls.mode=existingSecret` mounts `tls.existingSecretName`
- `tls.mode=disabled` is only valid when STARTTLS, SMTPS, and `smtp.requireTLS` are all disabled

NetworkPolicy values:

- `networkPolicy.ingressCIDRs[]`
- `networkPolicy.egressCIDRs[]`
- `networkPolicy.egressTCPPorts[]`

If you use nonstandard proxy or explicit provider endpoint ports, add those TCP ports to `networkPolicy.egressTCPPorts`. ACS endpoints hidden inside external secrets or connection strings remain operator-managed for that part.
When `networkPolicy.egressCIDRs` is configured, `networkPolicy.egressTCPPorts` must contain at least one TCP port.

Additional cert-manager settings:

- `certManager.issuerRef.group` (default `cert-manager.io`)
- `certManager.issuerRef.kind`
- `certManager.issuerRef.name`
- `certManager.altNames[]`
- `certManager.subject.organizations[]`
- `certManager.subject.organizationalUnits[]`
- `certManager.subject.countries[]`
- `certManager.subject.localities[]`
- `certManager.subject.provinces[]`
