#!/usr/bin/env bash

set -euo pipefail

: "${PUBLISH_KIND:?PUBLISH_KIND is required}"
: "${CHART_ARCHIVE:?CHART_ARCHIVE is required}"
: "${CHART_REGISTRY:?CHART_REGISTRY is required}"
: "${CHART_NAME:?CHART_NAME is required}"
: "${CHART_VERSION:?CHART_VERSION is required}"

if [[ ! -f "${CHART_ARCHIVE}" ]]; then
  echo "packaged chart not found: ${CHART_ARCHIVE}" >&2
  exit 1
fi

chart_ref="${CHART_REGISTRY}/${CHART_NAME}"
verify_dir=""

cleanup() {
  if [[ -n "${verify_dir}" && -d "${verify_dir}" ]]; then
    rm -rf "${verify_dir}"
  fi
}

trap cleanup EXIT

is_missing_chart_probe_error() {
  local error_output="$1"

  grep -Eiq 'not found|no such|manifest unknown|name unknown|does not exist' <<< "${error_output}"
}

probe_chart_version() {
  local -n out_state="$1"
  local probe_output

  out_state="error"

  if probe_output="$(helm show chart "${chart_ref}" --version "${CHART_VERSION}" 2>&1)"; then
    out_state="present"
    return 0
  fi

  if is_missing_chart_probe_error "${probe_output}"; then
    # shellcheck disable=SC2034
    out_state="missing"
    return 0
  fi

  echo "helm show chart failed for ${chart_ref}@${CHART_VERSION}: ${probe_output}" >&2
  return 1
}

archive_digest() {
  local archive_path="$1"

  sha256sum "${archive_path}" | awk '{print $1}'
}

verify_remote_chart_matches_local() {
  local pull_output
  local remote_archive
  local local_digest
  local remote_digest

  verify_dir="$(mktemp -d)"

  if ! pull_output="$(
    helm pull "${chart_ref}" \
      --version "${CHART_VERSION}" \
      --destination "${verify_dir}" 2>&1
  )"; then
    echo "helm pull failed for ${chart_ref}@${CHART_VERSION}: ${pull_output}" >&2
    return 1
  fi

  remote_archive="${verify_dir}/${CHART_NAME}-${CHART_VERSION}.tgz"
  if [[ ! -f "${remote_archive}" ]]; then
    echo "pulled chart archive not found: ${remote_archive}" >&2
    return 1
  fi

  local_digest="$(archive_digest "${CHART_ARCHIVE}")"
  remote_digest="$(archive_digest "${remote_archive}")"
  if [[ "${local_digest}" != "${remote_digest}" ]]; then
    echo "remote OCI chart ${chart_ref}@${CHART_VERSION} does not match local archive" >&2
    echo "local:  ${local_digest}" >&2
    echo "remote: ${remote_digest}" >&2
    return 1
  fi
}

if [[ "${PUBLISH_KIND}" == "stable" ]]; then
  chart_probe_state=""
  probe_chart_version chart_probe_state
  if [[ "${chart_probe_state}" == "present" ]]; then
    verify_remote_chart_matches_local
    exit 0
  fi
fi

helm push "${CHART_ARCHIVE}" "${CHART_REGISTRY}"
