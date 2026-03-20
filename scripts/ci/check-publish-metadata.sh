#!/usr/bin/env bash

set -euo pipefail

chart_dir="deploy/helm/smtp-cloud-relay"
chart_name="$(awk '$1 == "name:" { print $2; exit }' "${chart_dir}/Chart.yaml")"
chart_version="$(awk '$1 == "version:" { print $2; exit }' "${chart_dir}/Chart.yaml")"
registry="ghcr.io"
image_name="undy-io/smtp-cloud-relay"
fake_sha="0123456789abcdef0123456789abcdef01234567"
sha_tag="sha-${fake_sha::12}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

assert_equals() {
  local actual="$1"
  local expected="$2"
  local label="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "${label}: expected '${expected}', got '${actual}'" >&2
    exit 1
  fi
}

assert_contains_line() {
  local haystack="$1"
  local needle="$2"

  if ! grep -Fxq -- "${needle}" <<<"${haystack}"; then
    echo "expected output to contain line: ${needle}" >&2
    exit 1
  fi
}

assert_line_count() {
  local haystack="$1"
  local expected="$2"
  local label="$3"

  local actual
  actual="$(grep -cve '^[[:space:]]*$' <<<"${haystack}" || true)"
  assert_equals "${actual}" "${expected}" "${label}"
}

read_output_value() {
  local file="$1"
  local key="$2"

  awk -F= -v key="${key}" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "${file}"
}

read_output_multiline() {
  local file="$1"
  local key="$2"

  awk -v key="${key}" '
    $0 == key "<<EOF" { in_block = 1; next }
    in_block && $0 == "EOF" { exit }
    in_block { print }
  ' "${file}"
}

run_resolver() {
  local output_file="$1"
  shift

  env \
    CHART_DIR="${chart_dir}" \
    REGISTRY="${registry}" \
    IMAGE_NAME="${image_name}" \
    GITHUB_SHA="${fake_sha}" \
    GITHUB_OUTPUT="${output_file}" \
    "$@" \
    bash ./scripts/ci/resolve-publish-metadata.sh
}

expect_failure() {
  local name="$1"
  shift

  local output_file="${tmp_dir}/${name}.out"
  local log_file="${tmp_dir}/${name}.log"
  if run_resolver "${output_file}" "$@" >"${log_file}" 2>&1; then
    echo "expected resolve-publish-metadata.sh to fail for ${name}" >&2
    cat "${log_file}" >&2
    exit 1
  fi
}

nightly_output="${tmp_dir}/nightly.out"
run_resolver "${nightly_output}" \
  GITHUB_REF=refs/heads/main \
  GITHUB_REF_NAME=main \
  GITHUB_RUN_NUMBER=77 \
  GITHUB_RUN_ATTEMPT=1

nightly_version="${chart_version}-nightly.77.1"
nightly_build_refs="$(read_output_multiline "${nightly_output}" image_build_refs)"
nightly_alias_refs="$(read_output_multiline "${nightly_output}" image_alias_refs)"
assert_equals "$(read_output_value "${nightly_output}" publish_kind)" "nightly" "nightly publish kind"
assert_equals "$(read_output_value "${nightly_output}" chart_name)" "${chart_name}" "nightly chart name"
assert_equals "$(read_output_value "${nightly_output}" chart_version)" "${nightly_version}" "nightly chart version"
assert_equals "$(read_output_value "${nightly_output}" chart_app_version)" "${nightly_version}" "nightly chart appVersion"
assert_equals "$(read_output_value "${nightly_output}" chart_archive)" "dist/charts/${chart_name}-${nightly_version}.tgz" "nightly chart archive"
assert_equals "$(read_output_value "${nightly_output}" image_repository)" "${registry}/${image_name}" "nightly image repository"
assert_equals "$(read_output_value "${nightly_output}" image_canonical_ref)" "${registry}/${image_name}:${nightly_version}" "nightly canonical ref"
assert_equals "$(read_output_value "${nightly_output}" image_sha_ref)" "${registry}/${image_name}:${sha_tag}" "nightly sha ref"
assert_line_count "${nightly_build_refs}" 3 "nightly build ref count"
assert_line_count "${nightly_alias_refs}" 0 "nightly alias ref count"
assert_contains_line "${nightly_build_refs}" "${registry}/${image_name}:${nightly_version}"
assert_contains_line "${nightly_build_refs}" "${registry}/${image_name}:nightly"
assert_contains_line "${nightly_build_refs}" "${registry}/${image_name}:${sha_tag}"

nightly_retry_output="${tmp_dir}/nightly-retry.out"
run_resolver "${nightly_retry_output}" \
  GITHUB_REF=refs/heads/main \
  GITHUB_REF_NAME=main \
  GITHUB_RUN_NUMBER=77 \
  GITHUB_RUN_ATTEMPT=2
assert_equals "$(read_output_value "${nightly_retry_output}" chart_version)" "${chart_version}-nightly.77.2" "nightly retry chart version"

stable_output="${tmp_dir}/stable.out"
run_resolver "${stable_output}" \
  GITHUB_REF="refs/tags/${chart_version}" \
  GITHUB_REF_NAME="${chart_version}" \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

stable_build_refs="$(read_output_multiline "${stable_output}" image_build_refs)"
stable_alias_refs="$(read_output_multiline "${stable_output}" image_alias_refs)"
IFS=. read -r stable_major stable_minor _ <<< "${chart_version}"
assert_equals "$(read_output_value "${stable_output}" publish_kind)" "stable" "stable publish kind"
assert_equals "$(read_output_value "${stable_output}" chart_name)" "${chart_name}" "stable chart name"
assert_equals "$(read_output_value "${stable_output}" chart_version)" "${chart_version}" "stable chart version"
assert_equals "$(read_output_value "${stable_output}" chart_app_version)" "${chart_version}" "stable chart appVersion"
assert_equals "$(read_output_value "${stable_output}" image_repository)" "${registry}/${image_name}" "stable image repository"
assert_equals "$(read_output_value "${stable_output}" image_canonical_ref)" "${registry}/${image_name}:${chart_version}" "stable canonical ref"
assert_equals "$(read_output_value "${stable_output}" image_sha_ref)" "${registry}/${image_name}:${sha_tag}" "stable sha ref"
assert_line_count "${stable_build_refs}" 2 "stable build ref count"
assert_line_count "${stable_alias_refs}" 3 "stable alias ref count"
assert_contains_line "${stable_build_refs}" "${registry}/${image_name}:${chart_version}"
assert_contains_line "${stable_build_refs}" "${registry}/${image_name}:${sha_tag}"
assert_contains_line "${stable_alias_refs}" "${registry}/${image_name}:${stable_major}.${stable_minor}"
assert_contains_line "${stable_alias_refs}" "${registry}/${image_name}:${stable_major}"
assert_contains_line "${stable_alias_refs}" "${registry}/${image_name}:latest"

expect_failure prefixed-tag \
  GITHUB_REF="refs/tags/v${chart_version}" \
  GITHUB_REF_NAME="v${chart_version}" \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

expect_failure non-patch-tag \
  GITHUB_REF="refs/tags/${stable_major}.${stable_minor}" \
  GITHUB_REF_NAME="${stable_major}.${stable_minor}" \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1
