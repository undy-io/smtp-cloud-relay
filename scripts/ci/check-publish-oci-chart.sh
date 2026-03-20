#!/usr/bin/env bash

set -euo pipefail

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bin_dir="${tmp_dir}/bin"
registry_dir="${tmp_dir}/registry"
log_file="${tmp_dir}/helm.log"
chart_name="smtp-cloud-relay"
chart_registry="oci://ghcr.io/undy-io/charts"
mkdir -p "${bin_dir}" "${registry_dir}"
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

if [[ "${1:-}" == "push" ]]; then
  archive="${2:-}"
  registry="${3:-}"
  archive_name="$(basename "${archive}")"
  archive_stem="${archive_name%.tgz}"
  version="${archive_stem#${FAKE_HELM_CHART_NAME}-}"
  path="$(ref_path "${registry}/${FAKE_HELM_CHART_NAME}@${version}")"
  : > "${path}"
  printf 'push %s %s\n' "${archive}" "${registry}" >> "${FAKE_HELM_LOG}"
  exit 0
fi

echo "unexpected helm invocation: $*" >&2
exit 1
EOF

chmod +x "${bin_dir}/helm"

stable_archive="${tmp_dir}/${chart_name}-1.8.2.tgz"
nightly_archive="${tmp_dir}/${chart_name}-1.8.3-nightly.77.1.tgz"
: > "${stable_archive}"
: > "${nightly_archive}"

PATH="${bin_dir}:${PATH}" \
FAKE_HELM_REGISTRY_DIR="${registry_dir}" \
FAKE_HELM_LOG="${log_file}" \
FAKE_HELM_CHART_NAME="${chart_name}" \
PUBLISH_KIND=stable \
CHART_ARCHIVE="${stable_archive}" \
CHART_REGISTRY="${chart_registry}" \
CHART_NAME="${chart_name}" \
CHART_VERSION="1.8.2" \
  bash ./scripts/ci/publish-oci-chart.sh

assert_equals "$(grep -c '^push ' "${log_file}")" "1" "stable chart first push count"

: > "${log_file}"

probe_error_log="${tmp_dir}/probe-error.log"
if PATH="${bin_dir}:${PATH}" \
  FAKE_HELM_REGISTRY_DIR="${registry_dir}" \
  FAKE_HELM_LOG="${log_file}" \
  FAKE_HELM_CHART_NAME="${chart_name}" \
  FAKE_HELM_SHOW_MODE=error \
  PUBLISH_KIND=stable \
  CHART_ARCHIVE="${stable_archive}" \
  CHART_REGISTRY="${chart_registry}" \
  CHART_NAME="${chart_name}" \
  CHART_VERSION="1.8.2" \
  bash ./scripts/ci/publish-oci-chart.sh >"${probe_error_log}" 2>&1; then
  echo "expected OCI chart probe error to fail" >&2
  exit 1
fi

assert_contains "${probe_error_log}" "helm show chart failed"
assert_equals "$(grep -c '^push ' "${log_file}" || true)" "0" "stable chart probe error push count"

PATH="${bin_dir}:${PATH}" \
FAKE_HELM_REGISTRY_DIR="${registry_dir}" \
FAKE_HELM_LOG="${log_file}" \
FAKE_HELM_CHART_NAME="${chart_name}" \
PUBLISH_KIND=stable \
CHART_ARCHIVE="${stable_archive}" \
CHART_REGISTRY="${chart_registry}" \
CHART_NAME="${chart_name}" \
CHART_VERSION="1.8.2" \
  bash ./scripts/ci/publish-oci-chart.sh

assert_equals "$(grep -c '^push ' "${log_file}" || true)" "0" "stable chart rerun push count"

: > "${log_file}"

PATH="${bin_dir}:${PATH}" \
FAKE_HELM_REGISTRY_DIR="${registry_dir}" \
FAKE_HELM_LOG="${log_file}" \
FAKE_HELM_CHART_NAME="${chart_name}" \
PUBLISH_KIND=nightly \
CHART_ARCHIVE="${nightly_archive}" \
CHART_REGISTRY="${chart_registry}" \
CHART_NAME="${chart_name}" \
CHART_VERSION="1.8.3-nightly.77.1" \
  bash ./scripts/ci/publish-oci-chart.sh

assert_equals "$(grep -c '^push ' "${log_file}")" "1" "nightly chart push count"
