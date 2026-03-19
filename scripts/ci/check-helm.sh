#!/usr/bin/env bash

set -euo pipefail

chart_dir="deploy/helm/smtp-cloud-relay"
release_name="smtp-cloud-relay"

portable_tls=(--set-string tls.mode=existingSecret --set-string tls.existingSecretName=relay-tls)

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Fq -- "$pattern" "$file"; then
    echo "expected rendered output to contain: $pattern" >&2
    exit 1
  fi
}

assert_not_contains() {
  local file="$1"
  local pattern="$2"
  if grep -Fq -- "$pattern" "$file"; then
    echo "expected rendered output to not contain: $pattern" >&2
    exit 1
  fi
}

expect_render_failure() {
  local name="$1"
  shift

  local log_file="${tmp_dir}/${name}.log"
  if helm template "$release_name" "$chart_dir" "$@" >"$log_file" 2>&1; then
    echo "expected helm template failure for ${name}" >&2
    cat "$log_file" >&2
    exit 1
  fi
}

expect_render_failure_contains() {
  local name="$1"
  local pattern="$2"
  shift 2

  local log_file="${tmp_dir}/${name}.log"
  if helm template "$release_name" "$chart_dir" "$@" >"$log_file" 2>&1; then
    echo "expected helm template failure for ${name}" >&2
    cat "$log_file" >&2
    exit 1
  fi

  assert_contains "$log_file" "$pattern"
}

helm lint "$chart_dir" "${portable_tls[@]}"

positive_default="${tmp_dir}/default-cert-manager.yaml"
helm template "$release_name" "$chart_dir" --api-versions cert-manager.io/v1 >"$positive_default"
assert_contains "$positive_default" 'kind: Certificate'
assert_contains "$positive_default" 'secretName: smtp-cloud-relay-tls'
assert_contains "$positive_default" 'SMTP_LISTEN_ADDR: "0.0.0.0:2525"'
assert_contains "$positive_default" 'SMTPS_LISTEN_ADDR: "0.0.0.0:2465"'
assert_contains "$positive_default" 'HTTP_LISTEN_ADDR: "0.0.0.0:8080"'
assert_contains "$positive_default" 'containerPort: 2525'
assert_contains "$positive_default" 'containerPort: 2465'
assert_contains "$positive_default" 'containerPort: 8080'
assert_contains "$positive_default" 'port: 2525'
assert_contains "$positive_default" 'port: 2465'
assert_contains "$positive_default" 'port: 8080'

positive_existing_secret="${tmp_dir}/existing-secret.yaml"
helm template "$release_name" "$chart_dir" "${portable_tls[@]}" >"$positive_existing_secret"
assert_contains "$positive_existing_secret" 'secretName: relay-tls'

positive_no_tls="${tmp_dir}/no-tls.yaml"
helm template "$release_name" "$chart_dir" \
  --set-string tls.mode=disabled \
  --set smtp.starttlsEnabled=false \
  --set smtp.smtps.enabled=false \
  --set smtp.requireTLS=false >"$positive_no_tls"
assert_contains "$positive_no_tls" 'SMTP_LISTEN_ADDR: "0.0.0.0:2525"'
assert_contains "$positive_no_tls" 'SMTPS_LISTEN_ADDR: ""'
assert_not_contains "$positive_no_tls" 'kind: Certificate'
assert_not_contains "$positive_no_tls" 'name: smtp-tls'
assert_not_contains "$positive_no_tls" 'mountPath: /etc/smtp-tls'
assert_not_contains "$positive_no_tls" 'containerPort: 2465'
assert_not_contains "$positive_no_tls" 'port: 2465'

expect_render_failure tls-disabled-with-starttls --set-string tls.mode=disabled
expect_render_failure_contains plaintext-requires-disabled-cert-manager "tls.mode must be disabled when smtp.starttlsEnabled=false, smtp.smtps.enabled=false, and smtp.requireTLS=false" --set smtp.starttlsEnabled=false --set smtp.smtps.enabled=false --set smtp.requireTLS=false
expect_render_failure_contains plaintext-requires-disabled-existing-secret "tls.mode must be disabled when smtp.starttlsEnabled=false, smtp.smtps.enabled=false, and smtp.requireTLS=false" "${portable_tls[@]}" --set smtp.starttlsEnabled=false --set smtp.smtps.enabled=false --set smtp.requireTLS=false
expect_render_failure cert-manager-without-api
expect_render_failure existing-secret-without-name --set-string tls.mode=existingSecret
expect_render_failure require-tls-without-tls-listener "${portable_tls[@]}" --set smtp.requireTLS=true --set smtp.starttlsEnabled=false --set smtp.smtps.enabled=false
expect_render_failure blank-tls-cert-path "${portable_tls[@]}" --set-string tls.certFile=

empty_cidrs_values="${tmp_dir}/empty-cidrs.yaml"
cat >"$empty_cidrs_values" <<'VALUES'
smtp:
  allowedCIDRs: []
VALUES
expect_render_failure empty-allowed-cidrs "${portable_tls[@]}" -f "$empty_cidrs_values"
expect_render_failure require-auth-false "${portable_tls[@]}" --set smtp.requireAuth=false
expect_render_failure unsupported-auth-provider "${portable_tls[@]}" --set-string smtp.authProvider=oidc
expect_render_failure invalid-sender-policy "${portable_tls[@]}" --set-string senderPolicy.mode=permissive
expect_render_failure invalid-inflight "${portable_tls[@]}" --set smtp.maxInflightSends=0
expect_render_failure invalid-extra-trusted-ca "${portable_tls[@]}" --set extraTrustedCA.enabled=true
empty_egress_ports_values="${tmp_dir}/empty-egress-ports.yaml"
cat >"$empty_egress_ports_values" <<'VALUES'
networkPolicy:
  egressCIDRs:
    - 0.0.0.0/0
  egressTCPPorts: []
VALUES
expect_render_failure_contains empty-egress-tcp-ports "networkPolicy.egressTCPPorts must contain at least one entry when networkPolicy.egressCIDRs is configured" -f "$empty_egress_ports_values"
expect_render_failure proxy-port-not-allowed "${portable_tls[@]}" --set-string proxy.httpsProxy=http://proxy.internal:8443 --set networkPolicy.egressTCPPorts[0]=443
expect_render_failure ses-endpoint-port-not-allowed "${portable_tls[@]}" --set-string deliveryMode=ses --set-string ses.region=us-gov-west-1 --set-string ses.sender=no-reply@example.com --set-string ses.endpoint=https://ses.internal.example:8443 --set networkPolicy.egressTCPPorts[0]=443

make helm-package
