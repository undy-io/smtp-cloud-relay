#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
resolver_script="${script_dir}/resolve-publish-metadata.sh"

registry="ghcr.io"
image_name="undy-io/smtp-cloud-relay"
chart_name="smtp-cloud-relay"
fake_sha="0123456789abcdef0123456789abcdef01234567"
stable_sha_tag="sha-${fake_sha::12}"
nightly_sha_tag="nightly-sha-${fake_sha::12}"
chart_dir_rel="deploy/helm/${chart_name}"
main_branch="master"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

remote_dir="${tmp_dir}/remote.git"
work_dir="${tmp_dir}/work"
chart_dir="${work_dir}/${chart_dir_rel}"

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

assert_contains_line() {
  local haystack="$1"
  local needle="$2"

  if ! grep -Fxq -- "${needle}" <<< "${haystack}"; then
    echo "expected output to contain line: ${needle}" >&2
    exit 1
  fi
}

assert_line_count() {
  local haystack="$1"
  local expected="$2"
  local label="$3"
  local actual

  actual="$(grep -cve '^[[:space:]]*$' <<< "${haystack}" || true)"
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

write_chart() {
  local version="$1"

  mkdir -p "${chart_dir}"
  cat > "${chart_dir}/Chart.yaml" <<EOF
apiVersion: v2
name: ${chart_name}
version: ${version}
EOF
}

commit_and_tag() {
  local version="$1"

  write_chart "${version}"
  git -C "${work_dir}" add "${chart_dir_rel}/Chart.yaml"
  git -C "${work_dir}" commit -m "release ${version}" >/dev/null
  git -C "${work_dir}" tag "${version}"
}

run_resolver() {
  local checkout_ref="$1"
  local output_file="$2"
  shift 2

  (
    cd "${work_dir}"
    git checkout --detach "${checkout_ref}" >/dev/null 2>&1
    env \
      CHART_DIR="${chart_dir}" \
      REGISTRY="${registry}" \
      IMAGE_NAME="${image_name}" \
      GITHUB_SHA="${fake_sha}" \
      GITHUB_OUTPUT="${output_file}" \
      "$@" \
      bash "${resolver_script}"
  )
}

expect_failure() {
  local name="$1"
  local checkout_ref="$2"
  shift 2

  local output_file="${tmp_dir}/${name}.out"
  local log_file="${tmp_dir}/${name}.log"
  if run_resolver "${checkout_ref}" "${output_file}" "$@" > "${log_file}" 2>&1; then
    echo "expected resolve-publish-metadata.sh to fail for ${name}" >&2
    cat "${log_file}" >&2
    exit 1
  fi
}

git init --bare "${remote_dir}" >/dev/null
git clone "${remote_dir}" "${work_dir}" >/dev/null 2>&1
git -C "${work_dir}" config user.name "CI Test"
git -C "${work_dir}" config user.email "ci@example.com"
git -C "${work_dir}" checkout -b "${main_branch}" >/dev/null

commit_and_tag "1.8.2"
commit_and_tag "1.8.3"
commit_and_tag "1.9.0"

git -C "${work_dir}" push origin HEAD:"${main_branch}" --tags >/dev/null

git -C "${work_dir}" checkout --orphan unrelated >/dev/null 2>&1
git -C "${work_dir}" rm -rf . >/dev/null 2>&1 || true
write_chart "2.5.0"
git -C "${work_dir}" add "${chart_dir_rel}/Chart.yaml"
git -C "${work_dir}" commit -m "unrelated 2.5.0" >/dev/null
git -C "${work_dir}" tag "2.5.0"
git -C "${work_dir}" push origin HEAD:unrelated refs/tags/2.5.0 >/dev/null
git -C "${work_dir}" checkout "${main_branch}" >/dev/null 2>&1

nightly_output="${tmp_dir}/nightly.out"
run_resolver "${main_branch}" "${nightly_output}" \
  GITHUB_REF="refs/heads/${main_branch}" \
  GITHUB_REF_NAME="${main_branch}" \
  GITHUB_RUN_NUMBER=77 \
  GITHUB_RUN_ATTEMPT=1

nightly_version="1.9.0-nightly.77.1"
nightly_build_refs="$(read_output_multiline "${nightly_output}" image_build_refs)"
nightly_minor_blockers="$(read_output_multiline "${nightly_output}" image_minor_blocker_refs)"
nightly_major_blockers="$(read_output_multiline "${nightly_output}" image_major_blocker_refs)"
nightly_latest_blockers="$(read_output_multiline "${nightly_output}" image_latest_blocker_refs)"
assert_equals "$(read_output_value "${nightly_output}" publish_kind)" "nightly" "nightly publish kind"
assert_equals "$(read_output_value "${nightly_output}" chart_name)" "${chart_name}" "nightly chart name"
assert_equals "$(read_output_value "${nightly_output}" chart_version)" "${nightly_version}" "nightly chart version"
assert_equals "$(read_output_value "${nightly_output}" chart_app_version)" "${nightly_version}" "nightly chart appVersion"
assert_equals "$(read_output_value "${nightly_output}" chart_archive)" "dist/charts/${chart_name}-${nightly_version}.tgz" "nightly chart archive"
assert_equals "$(read_output_value "${nightly_output}" image_repository)" "${registry}/${image_name}" "nightly image repository"
assert_equals "$(read_output_value "${nightly_output}" image_canonical_ref)" "${registry}/${image_name}:${nightly_version}" "nightly canonical ref"
assert_equals "$(read_output_value "${nightly_output}" image_sha_ref)" "${registry}/${image_name}:${nightly_sha_tag}" "nightly sha ref"
assert_equals "$(read_output_value "${nightly_output}" image_promote_ref)" "${registry}/${image_name}:${nightly_sha_tag}" "nightly promote ref"
assert_equals "$(read_output_value "${nightly_output}" image_minor_alias_ref)" "" "nightly minor alias ref"
assert_equals "$(read_output_value "${nightly_output}" image_major_alias_ref)" "" "nightly major alias ref"
assert_equals "$(read_output_value "${nightly_output}" image_latest_alias_ref)" "" "nightly latest alias ref"
assert_line_count "${nightly_build_refs}" 3 "nightly build ref count"
assert_line_count "${nightly_minor_blockers}" 0 "nightly minor blocker count"
assert_line_count "${nightly_major_blockers}" 0 "nightly major blocker count"
assert_line_count "${nightly_latest_blockers}" 0 "nightly latest blocker count"
assert_contains_line "${nightly_build_refs}" "${registry}/${image_name}:${nightly_version}"
assert_contains_line "${nightly_build_refs}" "${registry}/${image_name}:nightly"
assert_contains_line "${nightly_build_refs}" "${registry}/${image_name}:${nightly_sha_tag}"

stable_182_output="${tmp_dir}/stable-182.out"
run_resolver "1.8.2" "${stable_182_output}" \
  GITHUB_REF=refs/tags/1.8.2 \
  GITHUB_REF_NAME=1.8.2 \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

stable_182_build_refs="$(read_output_multiline "${stable_182_output}" image_build_refs)"
stable_182_minor_blockers="$(read_output_multiline "${stable_182_output}" image_minor_blocker_refs)"
stable_182_major_blockers="$(read_output_multiline "${stable_182_output}" image_major_blocker_refs)"
stable_182_latest_blockers="$(read_output_multiline "${stable_182_output}" image_latest_blocker_refs)"
assert_equals "$(read_output_value "${stable_182_output}" publish_kind)" "stable" "1.8.2 publish kind"
assert_equals "$(read_output_value "${stable_182_output}" chart_name)" "${chart_name}" "1.8.2 chart name"
assert_equals "$(read_output_value "${stable_182_output}" chart_version)" "1.8.2" "1.8.2 chart version"
assert_equals "$(read_output_value "${stable_182_output}" chart_app_version)" "1.8.2" "1.8.2 chart appVersion"
assert_equals "$(read_output_value "${stable_182_output}" image_repository)" "${registry}/${image_name}" "1.8.2 image repository"
assert_equals "$(read_output_value "${stable_182_output}" image_canonical_ref)" "${registry}/${image_name}:1.8.2" "1.8.2 canonical ref"
assert_equals "$(read_output_value "${stable_182_output}" image_sha_ref)" "${registry}/${image_name}:${stable_sha_tag}" "1.8.2 sha ref"
assert_equals "$(read_output_value "${stable_182_output}" image_promote_ref)" "${registry}/${image_name}:${nightly_sha_tag}" "1.8.2 promote ref"
assert_equals "$(read_output_value "${stable_182_output}" image_minor_alias_ref)" "${registry}/${image_name}:1.8" "1.8.2 minor alias ref"
assert_equals "$(read_output_value "${stable_182_output}" image_major_alias_ref)" "${registry}/${image_name}:1" "1.8.2 major alias ref"
assert_equals "$(read_output_value "${stable_182_output}" image_latest_alias_ref)" "${registry}/${image_name}:latest" "1.8.2 latest alias ref"
assert_line_count "${stable_182_build_refs}" 2 "1.8.2 build ref count"
assert_line_count "${stable_182_minor_blockers}" 1 "1.8.2 minor blocker count"
assert_line_count "${stable_182_major_blockers}" 2 "1.8.2 major blocker count"
assert_line_count "${stable_182_latest_blockers}" 2 "1.8.2 latest blocker count"
assert_contains_line "${stable_182_build_refs}" "${registry}/${image_name}:1.8.2"
assert_contains_line "${stable_182_build_refs}" "${registry}/${image_name}:${stable_sha_tag}"
assert_contains_line "${stable_182_minor_blockers}" "${registry}/${image_name}:1.8.3"
assert_contains_line "${stable_182_major_blockers}" "${registry}/${image_name}:1.8.3"
assert_contains_line "${stable_182_major_blockers}" "${registry}/${image_name}:1.9.0"
assert_contains_line "${stable_182_latest_blockers}" "${registry}/${image_name}:1.8.3"
assert_contains_line "${stable_182_latest_blockers}" "${registry}/${image_name}:1.9.0"

stable_183_output="${tmp_dir}/stable-183.out"
run_resolver "1.8.3" "${stable_183_output}" \
  GITHUB_REF=refs/tags/1.8.3 \
  GITHUB_REF_NAME=1.8.3 \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

stable_183_minor_blockers="$(read_output_multiline "${stable_183_output}" image_minor_blocker_refs)"
stable_183_major_blockers="$(read_output_multiline "${stable_183_output}" image_major_blocker_refs)"
stable_183_latest_blockers="$(read_output_multiline "${stable_183_output}" image_latest_blocker_refs)"
assert_line_count "${stable_183_minor_blockers}" 0 "1.8.3 minor blocker count"
assert_line_count "${stable_183_major_blockers}" 1 "1.8.3 major blocker count"
assert_line_count "${stable_183_latest_blockers}" 1 "1.8.3 latest blocker count"
assert_contains_line "${stable_183_major_blockers}" "${registry}/${image_name}:1.9.0"
assert_contains_line "${stable_183_latest_blockers}" "${registry}/${image_name}:1.9.0"

stable_190_output="${tmp_dir}/stable-190.out"
run_resolver "1.9.0" "${stable_190_output}" \
  GITHUB_REF=refs/tags/1.9.0 \
  GITHUB_REF_NAME=1.9.0 \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

assert_line_count "$(read_output_multiline "${stable_190_output}" image_minor_blocker_refs)" 0 "1.9.0 minor blocker count"
assert_line_count "$(read_output_multiline "${stable_190_output}" image_major_blocker_refs)" 0 "1.9.0 major blocker count"
assert_line_count "$(read_output_multiline "${stable_190_output}" image_latest_blocker_refs)" 0 "1.9.0 latest blocker count"

expect_failure prefixed-tag \
  "${main_branch}" \
  GITHUB_REF=refs/tags/v1.9.0 \
  GITHUB_REF_NAME=v1.9.0 \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

expect_failure non-patch-tag \
  "${main_branch}" \
  GITHUB_REF=refs/tags/1.9 \
  GITHUB_REF_NAME=1.9 \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1

git -C "${work_dir}" update-ref -d "refs/remotes/origin/${main_branch}"

missing_main_ref_output="${tmp_dir}/missing-default-branch-ref.out"
missing_main_ref_log="${tmp_dir}/missing-default-branch-ref.log"
if run_resolver "1.8.2" "${missing_main_ref_output}" \
  GITHUB_REF=refs/tags/1.8.2 \
  GITHUB_REF_NAME=1.8.2 \
  GITHUB_RUN_NUMBER=88 \
  GITHUB_RUN_ATTEMPT=1 > "${missing_main_ref_log}" 2>&1; then
  echo "expected resolve-publish-metadata.sh to fail when origin/${main_branch} is missing" >&2
  exit 1
fi

assert_contains "${missing_main_ref_log}" "failed to resolve stable blocker source ref origin/${main_branch}"
if [[ -s "${missing_main_ref_output}" ]]; then
  echo "expected no stable metadata output when origin/${main_branch} is missing" >&2
  exit 1
fi
