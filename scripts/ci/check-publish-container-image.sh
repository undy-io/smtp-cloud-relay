#!/usr/bin/env bash

set -euo pipefail

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bin_dir="${tmp_dir}/bin"
registry_dir="${tmp_dir}/registry"
log_file="${tmp_dir}/docker.log"
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

assert_ref_digest() {
  local ref="$1"
  local expected="$2"
  local path

  path="${registry_dir}/$(printf '%s' "${ref}" | tr '/:@' '___')"
  if [[ ! -f "${path}" ]]; then
    echo "expected registry entry for ${ref}" >&2
    exit 1
  fi

  assert_equals "$(cat "${path}")" "${expected}" "${ref} digest"
}

assert_contains() {
  local file="$1"
  local pattern="$2"

  if ! grep -Fq -- "${pattern}" "${file}"; then
    echo "expected ${file} to contain: ${pattern}" >&2
    exit 1
  fi
}

cat > "${bin_dir}/docker" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

: "${FAKE_DOCKER_REGISTRY_DIR:?FAKE_DOCKER_REGISTRY_DIR is required}"
: "${FAKE_DOCKER_LOG:?FAKE_DOCKER_LOG is required}"

ref_path() {
  printf '%s/%s\n' "${FAKE_DOCKER_REGISTRY_DIR}" "$(printf '%s' "$1" | tr '/:@' '___')"
}

next_digest() {
  local counter_file="${FAKE_DOCKER_REGISTRY_DIR}/.counter"
  local counter=0

  if [[ -f "${counter_file}" ]]; then
    counter="$(cat "${counter_file}")"
  fi

  counter=$((counter + 1))
  printf '%s\n' "${counter}" > "${counter_file}"
  printf 'sha256:%064x\n' "${counter}"
}

if [[ "${1:-}" == "buildx" && "${2:-}" == "build" ]]; then
  shift 2
  tags=()
  while (($#)); do
    case "$1" in
      --tag)
        tags+=("$2")
        shift 2
        ;;
      --label)
        shift 2
        ;;
      --push|--provenance=false|--sbom=false)
        shift
        ;;
      *)
        shift
        ;;
    esac
  done

  digest="$(next_digest)"
  for ref in "${tags[@]}"; do
    printf '%s\n' "${digest}" > "$(ref_path "${ref}")"
  done
  printf 'build %s %s\n' "${digest}" "${tags[*]}" >> "${FAKE_DOCKER_LOG}"
  exit 0
fi

if [[ "${1:-}" == "buildx" && "${2:-}" == "imagetools" && "${3:-}" == "inspect" ]]; then
  ref="${4:-}"
  inspect_mode="${FAKE_DOCKER_INSPECT_MODE:-normal}"

  if [[ "${inspect_mode}" == "error" ]]; then
    echo "registry probe failed for ${ref}" >&2
    exit 2
  fi

  path="$(ref_path "${ref}")"
  if [[ ! -f "${path}" ]]; then
    echo "manifest not found for ${ref}" >&2
    exit 1
  fi

  printf 'Name: %s\n' "${ref}"
  printf 'Digest: %s\n' "$(cat "${path}")"
  exit 0
fi

if [[ "${1:-}" == "buildx" && "${2:-}" == "imagetools" && "${3:-}" == "create" ]]; then
  shift 3
  alias_ref=""
  source_ref=""
  while (($#)); do
    case "$1" in
      --tag)
        alias_ref="$2"
        shift 2
        ;;
      *)
        source_ref="$1"
        shift
        ;;
    esac
  done

  source_path="$(ref_path "${source_ref}")"
  if [[ ! -f "${source_path}" ]]; then
    echo "missing source ref ${source_ref}" >&2
    exit 1
  fi

  digest="$(cat "${source_path}")"
  printf '%s\n' "${digest}" > "$(ref_path "${alias_ref}")"
  printf 'create %s %s %s\n' "${alias_ref}" "${source_ref}" "${digest}" >> "${FAKE_DOCKER_LOG}"
  exit 0
fi

echo "unexpected docker invocation: $*" >&2
exit 1
EOF

chmod +x "${bin_dir}/docker"

stable_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.2"
stable_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:sha-0123456789ab"
stable_build_refs="${stable_canonical_ref}"$'\n'"${stable_sha_ref}"
stable_alias_refs="ghcr.io/undy-io/smtp-cloud-relay:1.8"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:1"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:latest"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${registry_dir}" \
FAKE_DOCKER_LOG="${log_file}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=stable \
IMAGE_BUILD_REFS="${stable_build_refs}" \
IMAGE_ALIAS_REFS="${stable_alias_refs}" \
IMAGE_CANONICAL_REF="${stable_canonical_ref}" \
IMAGE_SHA_REF="${stable_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="0123456789abcdef0123456789abcdef01234567" \
  bash ./scripts/ci/publish-container-image.sh

assert_equals "$(grep -c '^build ' "${log_file}")" "1" "stable first build count"
assert_equals "$(grep -c '^create ' "${log_file}")" "3" "stable first alias count"
stable_digest="sha256:$(printf '%064x' 1)"
assert_ref_digest "${stable_canonical_ref}" "${stable_digest}"
assert_ref_digest "${stable_sha_ref}" "${stable_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:1.8" "${stable_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:1" "${stable_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:latest" "${stable_digest}"

: > "${log_file}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${registry_dir}" \
FAKE_DOCKER_LOG="${log_file}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=stable \
IMAGE_BUILD_REFS="${stable_build_refs}" \
IMAGE_ALIAS_REFS="${stable_alias_refs}" \
IMAGE_CANONICAL_REF="${stable_canonical_ref}" \
IMAGE_SHA_REF="${stable_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="0123456789abcdef0123456789abcdef01234567" \
  bash ./scripts/ci/publish-container-image.sh

assert_equals "$(grep -c '^build ' "${log_file}" || true)" "0" "stable rerun build count"
assert_equals "$(grep -c '^create ' "${log_file}")" "3" "stable rerun alias count"

printf 'sha256:%064x\n' 99 > "${registry_dir}/$(printf '%s' "${stable_sha_ref}" | tr '/:@' '___')"
: > "${log_file}"
inconsistent_log="${tmp_dir}/stable-inconsistent.log"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_REGISTRY_DIR="${registry_dir}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_INSPECT_MODE=normal \
  REGISTRY_PROBE_RETRIES=1 \
  REGISTRY_PROBE_DELAY_SECONDS=0 \
  PUBLISH_KIND=stable \
  IMAGE_BUILD_REFS="${stable_build_refs}" \
  IMAGE_ALIAS_REFS="${stable_alias_refs}" \
  IMAGE_CANONICAL_REF="${stable_canonical_ref}" \
  IMAGE_SHA_REF="${stable_sha_ref}" \
  bash ./scripts/ci/publish-container-image.sh >"${inconsistent_log}" 2>&1; then
  echo "expected inconsistent stable image state to fail" >&2
  exit 1
fi

grep -Fq -- "stable image state is inconsistent" "${inconsistent_log}" || {
  echo "expected inconsistent stable image error" >&2
  exit 1
}
assert_equals "$(grep -c '^build ' "${log_file}" || true)" "0" "inconsistent stable build count"
assert_equals "$(grep -c '^create ' "${log_file}" || true)" "0" "inconsistent stable alias count"

probe_error_log="${tmp_dir}/probe-error.log"
: > "${log_file}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_REGISTRY_DIR="${registry_dir}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_INSPECT_MODE=error \
  REGISTRY_PROBE_RETRIES=1 \
  REGISTRY_PROBE_DELAY_SECONDS=0 \
  PUBLISH_KIND=stable \
  IMAGE_BUILD_REFS="${stable_build_refs}" \
  IMAGE_ALIAS_REFS="${stable_alias_refs}" \
  IMAGE_CANONICAL_REF="${stable_canonical_ref}" \
  IMAGE_SHA_REF="${stable_sha_ref}" \
  bash ./scripts/ci/publish-container-image.sh >"${probe_error_log}" 2>&1; then
  echo "expected probe error path to fail" >&2
  exit 1
fi

assert_contains "${probe_error_log}" "docker buildx imagetools inspect failed"
assert_equals "$(grep -c '^build ' "${log_file}" || true)" "0" "probe error build count"
assert_equals "$(grep -c '^create ' "${log_file}" || true)" "0" "probe error alias count"

nightly_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.3-nightly.77.1"
nightly_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:sha-fedcba987654"
nightly_build_refs="${nightly_canonical_ref}"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:nightly"$'\n'"${nightly_sha_ref}"

: > "${log_file}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${registry_dir}" \
FAKE_DOCKER_LOG="${log_file}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=nightly \
IMAGE_BUILD_REFS="${nightly_build_refs}" \
IMAGE_ALIAS_REFS="" \
IMAGE_CANONICAL_REF="${nightly_canonical_ref}" \
IMAGE_SHA_REF="${nightly_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="fedcba9876543210fedcba9876543210fedcba98" \
  bash ./scripts/ci/publish-container-image.sh

nightly_digest="sha256:$(printf '%064x' 2)"
assert_equals "$(grep -c '^build ' "${log_file}")" "1" "nightly build count"
assert_equals "$(grep -c '^create ' "${log_file}" || true)" "0" "nightly alias count"
assert_ref_digest "${nightly_canonical_ref}" "${nightly_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:nightly" "${nightly_digest}"
assert_ref_digest "${nightly_sha_ref}" "${nightly_digest}"
