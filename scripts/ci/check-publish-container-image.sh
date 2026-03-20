#!/usr/bin/env bash

set -euo pipefail

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

bin_dir="${tmp_dir}/bin"
registry_dir="${tmp_dir}/registry"
current_registry_dir="${registry_dir}"
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

  path="${current_registry_dir}/$(printf '%s' "${ref}" | tr '/:@' '___')"
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

assert_missing_ref() {
  local ref="$1"
  local path

  path="${current_registry_dir}/$(printf '%s' "${ref}" | tr '/:@' '___')"
  if [[ -f "${path}" ]]; then
    echo "expected registry entry for ${ref} to be absent" >&2
    exit 1
  fi
}

write_ref_digest() {
  local ref="$1"
  local digest="$2"

  printf '%s\n' "${digest}" > "${current_registry_dir}/$(printf '%s' "${ref}" | tr '/:@' '___')"
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
  tags=()
  source_ref=""
  while (($#)); do
    case "$1" in
      --tag)
        tags+=("$2")
        shift 2
        ;;
      *)
        source_ref="$1"
        shift
        ;;
    esac
  done

  if [[ "${#tags[@]}" -eq 0 ]]; then
    echo "missing tag arguments for create" >&2
    exit 1
  fi

  source_path="$(ref_path "${source_ref}")"
  if [[ ! -f "${source_path}" ]]; then
    echo "missing source ref ${source_ref}" >&2
    exit 1
  fi

  digest="$(cat "${source_path}")"
  printf 'create-call %s %s\n' "${source_ref}" "${tags[*]}" >> "${FAKE_DOCKER_LOG}"

  create_mode="${FAKE_DOCKER_CREATE_MODE:-normal}"
  create_limit="${#tags[@]}"
  create_exit_code=0
  case "${create_mode}" in
    normal)
      ;;
    fail_after_first_tag)
      create_limit=1
      create_exit_code=2
      ;;
    *)
      echo "unsupported FAKE_DOCKER_CREATE_MODE: ${create_mode}" >&2
      exit 1
      ;;
  esac

  for (( i = 0; i < create_limit; i += 1 )); do
    tag_ref="${tags[$i]}"
    printf '%s\n' "${digest}" > "$(ref_path "${tag_ref}")"
    printf 'create %s %s %s\n' "${tag_ref}" "${source_ref}" "${digest}" >> "${FAKE_DOCKER_LOG}"
  done

  if [[ "${create_exit_code}" != "0" ]]; then
    echo "create failed for ${source_ref}" >&2
    exit "${create_exit_code}"
  fi

  exit 0
fi

echo "unexpected docker invocation: $*" >&2
exit 1
EOF

chmod +x "${bin_dir}/docker"

stable_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.2"
stable_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:sha-0123456789ab"
stable_promote_ref="ghcr.io/undy-io/smtp-cloud-relay:nightly-sha-0123456789ab"
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
IMAGE_PROMOTE_REF="${stable_promote_ref}" \
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
IMAGE_PROMOTE_REF="${stable_promote_ref}" \
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
  IMAGE_PROMOTE_REF="${stable_promote_ref}" \
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
  IMAGE_PROMOTE_REF="${stable_promote_ref}" \
  bash ./scripts/ci/publish-container-image.sh >"${probe_error_log}" 2>&1; then
  echo "expected probe error path to fail" >&2
  exit 1
fi

assert_contains "${probe_error_log}" "docker buildx imagetools inspect failed"
assert_equals "$(grep -c '^build ' "${log_file}" || true)" "0" "probe error build count"
assert_equals "$(grep -c '^create ' "${log_file}" || true)" "0" "probe error alias count"

promotion_registry_dir="${tmp_dir}/promotion-registry"
promotion_log="${tmp_dir}/promotion.log"
mkdir -p "${promotion_registry_dir}"
: > "${promotion_log}"
current_registry_dir="${promotion_registry_dir}"

nightly_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.3-nightly.77.1"
nightly_retry_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.3-nightly.77.2"
nightly_post_stable_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.3-nightly.77.3"
nightly_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:nightly-sha-fedcba987654"
nightly_build_refs="${nightly_canonical_ref}"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:nightly"$'\n'"${nightly_sha_ref}"
nightly_retry_build_refs="${nightly_retry_canonical_ref}"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:nightly"$'\n'"${nightly_sha_ref}"
nightly_post_stable_build_refs="${nightly_post_stable_canonical_ref}"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:nightly"$'\n'"${nightly_sha_ref}"

promotion_stable_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:1.8.3"
promotion_stable_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:sha-fedcba987654"
promotion_stable_build_refs="${promotion_stable_canonical_ref}"$'\n'"${promotion_stable_sha_ref}"
promotion_stable_alias_refs="ghcr.io/undy-io/smtp-cloud-relay:1.8"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:1"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:latest"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${promotion_registry_dir}" \
FAKE_DOCKER_LOG="${promotion_log}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=nightly \
IMAGE_BUILD_REFS="${nightly_build_refs}" \
IMAGE_ALIAS_REFS="" \
IMAGE_CANONICAL_REF="${nightly_canonical_ref}" \
IMAGE_SHA_REF="${nightly_sha_ref}" \
IMAGE_PROMOTE_REF="${nightly_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="fedcba9876543210fedcba9876543210fedcba98" \
  bash ./scripts/ci/publish-container-image.sh

nightly_digest="sha256:$(printf '%064x' 1)"
assert_equals "$(grep -c '^build ' "${promotion_log}")" "1" "nightly first build count"
assert_equals "$(grep -c '^create ' "${promotion_log}" || true)" "0" "nightly first alias count"
assert_ref_digest "${nightly_canonical_ref}" "${nightly_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:nightly" "${nightly_digest}"
assert_ref_digest "${nightly_sha_ref}" "${nightly_digest}"

: > "${promotion_log}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${promotion_registry_dir}" \
FAKE_DOCKER_LOG="${promotion_log}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=nightly \
IMAGE_BUILD_REFS="${nightly_retry_build_refs}" \
IMAGE_ALIAS_REFS="" \
IMAGE_CANONICAL_REF="${nightly_retry_canonical_ref}" \
IMAGE_SHA_REF="${nightly_sha_ref}" \
IMAGE_PROMOTE_REF="${nightly_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="fedcba9876543210fedcba9876543210fedcba98" \
  bash ./scripts/ci/publish-container-image.sh

nightly_retry_digest="sha256:$(printf '%064x' 2)"
assert_equals "$(grep -c '^build ' "${promotion_log}")" "1" "nightly rerun build count"
assert_equals "$(grep -c '^create ' "${promotion_log}" || true)" "0" "nightly rerun alias count"
assert_ref_digest "${nightly_canonical_ref}" "${nightly_digest}"
assert_ref_digest "${nightly_retry_canonical_ref}" "${nightly_retry_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:nightly" "${nightly_retry_digest}"
assert_ref_digest "${nightly_sha_ref}" "${nightly_retry_digest}"

: > "${promotion_log}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${promotion_registry_dir}" \
FAKE_DOCKER_LOG="${promotion_log}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=stable \
IMAGE_BUILD_REFS="${promotion_stable_build_refs}" \
IMAGE_ALIAS_REFS="${promotion_stable_alias_refs}" \
IMAGE_CANONICAL_REF="${promotion_stable_canonical_ref}" \
IMAGE_SHA_REF="${promotion_stable_sha_ref}" \
IMAGE_PROMOTE_REF="${nightly_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="fedcba9876543210fedcba9876543210fedcba98" \
  bash ./scripts/ci/publish-container-image.sh

assert_equals "$(grep -c '^build ' "${promotion_log}" || true)" "0" "stable-after-nightly build count"
assert_equals "$(grep -c '^create ' "${promotion_log}")" "5" "stable-after-nightly create count"
assert_equals "$(grep -c '^create-call ' "${promotion_log}")" "2" "stable-after-nightly create call count"
assert_contains "${promotion_log}" "create-call ${nightly_sha_ref} ${promotion_stable_canonical_ref} ${promotion_stable_sha_ref}"
assert_contains "${promotion_log}" "create-call ${promotion_stable_canonical_ref} ghcr.io/undy-io/smtp-cloud-relay:1.8 ghcr.io/undy-io/smtp-cloud-relay:1 ghcr.io/undy-io/smtp-cloud-relay:latest"
assert_ref_digest "${promotion_stable_canonical_ref}" "${nightly_retry_digest}"
assert_ref_digest "${promotion_stable_sha_ref}" "${nightly_retry_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:1.8" "${nightly_retry_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:1" "${nightly_retry_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:latest" "${nightly_retry_digest}"

: > "${promotion_log}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${promotion_registry_dir}" \
FAKE_DOCKER_LOG="${promotion_log}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=nightly \
IMAGE_BUILD_REFS="${nightly_post_stable_build_refs}" \
IMAGE_ALIAS_REFS="" \
IMAGE_CANONICAL_REF="${nightly_post_stable_canonical_ref}" \
IMAGE_SHA_REF="${nightly_sha_ref}" \
IMAGE_PROMOTE_REF="${nightly_sha_ref}" \
GITHUB_REPOSITORY="undy-io/smtp-cloud-relay" \
GITHUB_SHA="fedcba9876543210fedcba9876543210fedcba98" \
  bash ./scripts/ci/publish-container-image.sh

nightly_post_stable_digest="sha256:$(printf '%064x' 3)"
assert_equals "$(grep -c '^build ' "${promotion_log}")" "1" "nightly-after-stable build count"
assert_equals "$(grep -c '^create ' "${promotion_log}" || true)" "0" "nightly-after-stable alias count"
assert_ref_digest "${nightly_post_stable_canonical_ref}" "${nightly_post_stable_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:nightly" "${nightly_post_stable_digest}"
assert_ref_digest "${nightly_sha_ref}" "${nightly_post_stable_digest}"
assert_ref_digest "${promotion_stable_canonical_ref}" "${nightly_retry_digest}"
assert_ref_digest "${promotion_stable_sha_ref}" "${nightly_retry_digest}"

partial_registry_dir="${tmp_dir}/partial-registry"
partial_log="${tmp_dir}/partial.log"
mkdir -p "${partial_registry_dir}"
: > "${partial_log}"
current_registry_dir="${partial_registry_dir}"

partial_nightly_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:nightly-sha-abcdefabcdef"
partial_stable_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:2.0.0"
partial_stable_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:sha-abcdefabcdef"
partial_stable_build_refs="${partial_stable_canonical_ref}"$'\n'"${partial_stable_sha_ref}"
partial_stable_alias_refs="ghcr.io/undy-io/smtp-cloud-relay:2.0"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:2"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:latest"
partial_initial_digest="sha256:$(printf '%064x' 7)"
partial_moved_nightly_digest="sha256:$(printf '%064x' 8)"
write_ref_digest "${partial_nightly_sha_ref}" "${partial_initial_digest}"

partial_failure_log="${tmp_dir}/partial-failure.log"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_REGISTRY_DIR="${partial_registry_dir}" \
  FAKE_DOCKER_LOG="${partial_log}" \
  FAKE_DOCKER_CREATE_MODE=fail_after_first_tag \
  REGISTRY_PROBE_RETRIES=1 \
  REGISTRY_PROBE_DELAY_SECONDS=0 \
  PUBLISH_KIND=stable \
  IMAGE_BUILD_REFS="${partial_stable_build_refs}" \
  IMAGE_ALIAS_REFS="${partial_stable_alias_refs}" \
  IMAGE_CANONICAL_REF="${partial_stable_canonical_ref}" \
  IMAGE_SHA_REF="${partial_stable_sha_ref}" \
  IMAGE_PROMOTE_REF="${partial_nightly_sha_ref}" \
  bash ./scripts/ci/publish-container-image.sh >"${partial_failure_log}" 2>&1; then
  echo "expected partial promotion failure to fail" >&2
  exit 1
fi

assert_contains "${partial_failure_log}" "promote nightly manifest to stable refs did not produce matching digests"
assert_equals "$(grep -c '^build ' "${partial_log}" || true)" "0" "partial promotion failure build count"
assert_ref_digest "${partial_stable_canonical_ref}" "${partial_initial_digest}"
assert_missing_ref "${partial_stable_sha_ref}"
write_ref_digest "${partial_nightly_sha_ref}" "${partial_moved_nightly_digest}"

: > "${partial_log}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${partial_registry_dir}" \
FAKE_DOCKER_LOG="${partial_log}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=stable \
IMAGE_BUILD_REFS="${partial_stable_build_refs}" \
IMAGE_ALIAS_REFS="${partial_stable_alias_refs}" \
IMAGE_CANONICAL_REF="${partial_stable_canonical_ref}" \
IMAGE_SHA_REF="${partial_stable_sha_ref}" \
IMAGE_PROMOTE_REF="${partial_nightly_sha_ref}" \
  bash ./scripts/ci/publish-container-image.sh

assert_equals "$(grep -c '^build ' "${partial_log}" || true)" "0" "partial promotion repair build count"
assert_equals "$(grep -c '^create-call ' "${partial_log}")" "2" "partial promotion repair create call count"
assert_contains "${partial_log}" "create-call ${partial_stable_canonical_ref} ${partial_stable_sha_ref}"
assert_ref_digest "${partial_stable_canonical_ref}" "${partial_initial_digest}"
assert_ref_digest "${partial_stable_sha_ref}" "${partial_initial_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:2.0" "${partial_initial_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:2" "${partial_initial_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:latest" "${partial_initial_digest}"
assert_ref_digest "${partial_nightly_sha_ref}" "${partial_moved_nightly_digest}"

sha_only_registry_dir="${tmp_dir}/sha-only-registry"
sha_only_log="${tmp_dir}/sha-only.log"
mkdir -p "${sha_only_registry_dir}"
: > "${sha_only_log}"
current_registry_dir="${sha_only_registry_dir}"

sha_only_stable_canonical_ref="ghcr.io/undy-io/smtp-cloud-relay:3.0.0"
sha_only_stable_sha_ref="ghcr.io/undy-io/smtp-cloud-relay:sha-123451234512"
sha_only_promote_ref="ghcr.io/undy-io/smtp-cloud-relay:nightly-sha-123451234512"
sha_only_build_refs="${sha_only_stable_canonical_ref}"$'\n'"${sha_only_stable_sha_ref}"
sha_only_alias_refs="ghcr.io/undy-io/smtp-cloud-relay:3.0"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:3"$'\n'"ghcr.io/undy-io/smtp-cloud-relay:latest"
sha_only_digest="sha256:$(printf '%064x' 11)"
sha_only_promote_digest="sha256:$(printf '%064x' 12)"
write_ref_digest "${sha_only_stable_sha_ref}" "${sha_only_digest}"
write_ref_digest "${sha_only_promote_ref}" "${sha_only_promote_digest}"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_REGISTRY_DIR="${sha_only_registry_dir}" \
FAKE_DOCKER_LOG="${sha_only_log}" \
REGISTRY_PROBE_RETRIES=1 \
REGISTRY_PROBE_DELAY_SECONDS=0 \
PUBLISH_KIND=stable \
IMAGE_BUILD_REFS="${sha_only_build_refs}" \
IMAGE_ALIAS_REFS="${sha_only_alias_refs}" \
IMAGE_CANONICAL_REF="${sha_only_stable_canonical_ref}" \
IMAGE_SHA_REF="${sha_only_stable_sha_ref}" \
IMAGE_PROMOTE_REF="${sha_only_promote_ref}" \
  bash ./scripts/ci/publish-container-image.sh

assert_equals "$(grep -c '^build ' "${sha_only_log}" || true)" "0" "sha-only repair build count"
assert_equals "$(grep -c '^create-call ' "${sha_only_log}")" "2" "sha-only repair create call count"
assert_contains "${sha_only_log}" "create-call ${sha_only_stable_sha_ref} ${sha_only_stable_canonical_ref}"
assert_ref_digest "${sha_only_stable_canonical_ref}" "${sha_only_digest}"
assert_ref_digest "${sha_only_stable_sha_ref}" "${sha_only_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:3.0" "${sha_only_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:3" "${sha_only_digest}"
assert_ref_digest "ghcr.io/undy-io/smtp-cloud-relay:latest" "${sha_only_digest}"
assert_ref_digest "${sha_only_promote_ref}" "${sha_only_promote_digest}"
