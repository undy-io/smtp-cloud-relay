#!/usr/bin/env bash

set -euo pipefail

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bin_dir="${tmp_dir}/bin"
log_file="${tmp_dir}/helm.log"
chart_name="smtp-cloud-relay"
chart_registry="oci://ghcr.io/undy-io/charts"
current_registry_dir="${tmp_dir}/registry"
mkdir -p "${bin_dir}" "${current_registry_dir}"
: > "${log_file}"

assert_equals() {
  local actual="$1"
  local expected="$2"
  local label="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "${label}: expected '${expected}', got '${actual}'" >&2
    exit 1
  fi
}

assert_contains() {
  local file="$1"
  local pattern="$2"

  if ! grep -Fq -- "${pattern}" "${file}"; then
    echo "expected ${file} to contain: ${pattern}" >&2
    exit 1
  fi
}

assert_registry_archive_matches() {
  local version="$1"
  local local_archive="$2"
  local remote_archive

  remote_archive="$(registry_chart_path "${version}")"
  if [[ ! -f "${remote_archive}" ]]; then
    echo "expected registry archive for ${version}" >&2
    exit 1
  fi

  if ! cmp -s "${local_archive}" "${remote_archive}"; then
    echo "expected registry archive for ${version} to match ${local_archive}" >&2
    exit 1
  fi
}

assert_missing_registry_archive() {
  local version="$1"

  if [[ -f "$(registry_chart_path "${version}")" ]]; then
    echo "expected registry archive for ${version} to be absent" >&2
    exit 1
  fi
}

registry_chart_path() {
  printf '%s/%s\n' \
    "${current_registry_dir}" \
    "$(printf '%s' "${chart_registry}/${chart_name}@$1" | tr '/:@' '____')"
}

run_publish_oci() {
  local publish_kind="$1"
  local chart_archive="$2"
  local chart_version="$3"

  PATH="${bin_dir}:${PATH}" \
  FAKE_HELM_REGISTRY_DIR="${current_registry_dir}" \
  FAKE_HELM_LOG="${log_file}" \
  FAKE_HELM_CHART_NAME="${chart_name}" \
  PUBLISH_KIND="${publish_kind}" \
  CHART_ARCHIVE="${chart_archive}" \
  CHART_REGISTRY="${chart_registry}" \
  CHART_NAME="${chart_name}" \
  CHART_VERSION="${chart_version}" \
    bash ./scripts/ci/publish-oci-chart.sh
}

cat > "${bin_dir}/helm" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

: "${FAKE_HELM_REGISTRY_DIR:?FAKE_HELM_REGISTRY_DIR is required}"
: "${FAKE_HELM_LOG:?FAKE_HELM_LOG is required}"
: "${FAKE_HELM_CHART_NAME:?FAKE_HELM_CHART_NAME is required}"

ref_path() {
  printf '%s/%s\n' "${FAKE_HELM_REGISTRY_DIR}" "$(printf '%s' "$1" | tr '/:@' '____')"
}

if [[ "${1:-}" == "show" && "${2:-}" == "chart" ]]; then
  ref="${3:-}"
  show_mode="${FAKE_HELM_SHOW_MODE:-normal}"
  shift 3
  version=""
  while (($#)); do
    case "$1" in
      --version)
        version="$2"
        shift 2
        ;;
      *)
        shift
      ;;
    esac
  done

  if [[ "${show_mode}" == "error" ]]; then
    echo "Error: registry unavailable for ${ref}@${version}" >&2
    exit 2
  fi

  path="$(ref_path "${ref}@${version}")"
  if [[ ! -f "${path}" ]]; then
    echo "Error: chart not found for ${ref}@${version}" >&2
    exit 1
  fi

  printf 'name: %s\n' "${FAKE_HELM_CHART_NAME}"
  printf 'version: %s\n' "${version}"
  exit 0
fi

if [[ "${1:-}" == "pull" ]]; then
  ref="${2:-}"
  pull_mode="${FAKE_HELM_PULL_MODE:-normal}"
  shift 2
  version=""
  destination=""
  while (($#)); do
    case "$1" in
      --version)
        version="$2"
        shift 2
        ;;
      --destination)
        destination="$2"
        shift 2
        ;;
      *)
        shift
      ;;
    esac
  done

  if [[ "${pull_mode}" == "error" ]]; then
    echo "Error: registry download unavailable for ${ref}@${version}" >&2
    exit 2
  fi

  path="$(ref_path "${ref}@${version}")"
  if [[ ! -f "${path}" ]]; then
    echo "Error: chart not found for ${ref}@${version}" >&2
    exit 1
  fi

  mkdir -p "${destination}"
  cp "${path}" "${destination}/${FAKE_HELM_CHART_NAME}-${version}.tgz"
  printf 'pull %s %s %s\n' "${ref}" "${version}" "${destination}" >> "${FAKE_HELM_LOG}"
  exit 0
fi

if [[ "${1:-}" == "push" ]]; then
  archive="${2:-}"
  registry="${3:-}"
  archive_name="$(basename "${archive}")"
  archive_stem="${archive_name%.tgz}"
  version="${archive_stem#${FAKE_HELM_CHART_NAME}-}"
  path="$(ref_path "${registry}/${FAKE_HELM_CHART_NAME}@${version}")"
  cp "${archive}" "${path}"
  printf 'push %s %s\n' "${archive}" "${registry}" >> "${FAKE_HELM_LOG}"
  exit 0
fi

echo "unexpected helm invocation: $*" >&2
exit 1
EOF

chmod +x "${bin_dir}/helm"

stable_archive="${tmp_dir}/${chart_name}-1.8.2.tgz"
nightly_archive="${tmp_dir}/${chart_name}-1.8.3-nightly.77.1.tgz"
printf 'stable-chart-bytes-v1\n' > "${stable_archive}"
printf 'nightly-chart-bytes-v1\n' > "${nightly_archive}"

current_registry_dir="${tmp_dir}/registry-stable-first"
mkdir -p "${current_registry_dir}"
run_publish_oci stable "${stable_archive}" "1.8.2"

assert_equals "$(grep -c '^push ' "${log_file}")" "1" "stable chart first push count"
assert_equals "$(grep -c '^pull ' "${log_file}" || true)" "0" "stable chart first pull count"
assert_registry_archive_matches "1.8.2" "${stable_archive}"

: > "${log_file}"

run_publish_oci stable "${stable_archive}" "1.8.2"

assert_equals "$(grep -c '^push ' "${log_file}" || true)" "0" "stable chart rerun push count"
assert_equals "$(grep -c '^pull ' "${log_file}")" "1" "stable chart rerun pull count"
assert_contains "${log_file}" "pull ${chart_registry}/${chart_name} 1.8.2"

: > "${log_file}"

printf 'corrupted-remote-chart\n' > "$(registry_chart_path "1.8.2")"
mismatch_log="${tmp_dir}/mismatch.log"
if run_publish_oci stable "${stable_archive}" "1.8.2" >"${mismatch_log}" 2>&1; then
  echo "expected stable OCI mismatch to fail" >&2
  exit 1
fi

assert_contains "${mismatch_log}" "does not match local archive"
assert_equals "$(grep -c '^push ' "${log_file}" || true)" "0" "stable chart mismatch push count"
assert_equals "$(grep -c '^pull ' "${log_file}")" "1" "stable chart mismatch pull count"

: > "${log_file}"

probe_error_log="${tmp_dir}/probe-error.log"
if FAKE_HELM_SHOW_MODE=error \
  run_publish_oci stable "${stable_archive}" "1.8.2" >"${probe_error_log}" 2>&1; then
  echo "expected OCI chart probe error to fail" >&2
  exit 1
fi

assert_contains "${probe_error_log}" "helm show chart failed"
assert_equals "$(grep -c '^push ' "${log_file}" || true)" "0" "stable chart probe error push count"
assert_equals "$(grep -c '^pull ' "${log_file}" || true)" "0" "stable chart probe error pull count"

: > "${log_file}"

pull_error_log="${tmp_dir}/pull-error.log"
if FAKE_HELM_PULL_MODE=error \
  run_publish_oci stable "${stable_archive}" "1.8.2" >"${pull_error_log}" 2>&1; then
  echo "expected OCI chart pull verification error to fail" >&2
  exit 1
fi

assert_contains "${pull_error_log}" "helm pull failed"
assert_equals "$(grep -c '^push ' "${log_file}" || true)" "0" "stable chart pull error push count"
assert_equals "$(grep -c '^pull ' "${log_file}" || true)" "0" "stable chart pull error count"

: > "${log_file}"

current_registry_dir="${tmp_dir}/registry-nightly"
mkdir -p "${current_registry_dir}"
run_publish_oci nightly "${nightly_archive}" "1.8.3-nightly.77.1"

assert_equals "$(grep -c '^push ' "${log_file}")" "1" "nightly chart push count"
assert_equals "$(grep -c '^pull ' "${log_file}" || true)" "0" "nightly chart pull count"
assert_registry_archive_matches "1.8.3-nightly.77.1" "${nightly_archive}"
