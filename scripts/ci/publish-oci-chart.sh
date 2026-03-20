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

if [[ "${PUBLISH_KIND}" == "stable" ]]; then
  chart_probe_state=""
  probe_chart_version chart_probe_state
  if [[ "${chart_probe_state}" == "present" ]]; then
    exit 0
  fi
fi

helm push "${CHART_ARCHIVE}" "${CHART_REGISTRY}"
