#!/usr/bin/env bash

set -euo pipefail

chart_dir="deploy/helm/smtp-cloud-relay"
chart_name="$(awk '$1 == "name:" { print $2; exit }' "${chart_dir}/Chart.yaml")"
repo_url="https://example.com/charts"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

assert_contains() {
  local file="$1"
  local pattern="$2"

  if ! grep -Fq -- "${pattern}" "${file}"; then
    echo "expected ${file} to contain: ${pattern}" >&2
    exit 1
  fi
}

assert_equals() {
  local actual="$1"
  local expected="$2"
  local label="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "${label}: expected '${expected}', got '${actual}'" >&2
    exit 1
  fi
}

assert_output_value() {
  local file="$1"
  local key="$2"
  local expected="$3"

  local actual
  local match_count

  match_count="$(awk -F= -v key="${key}" '$1 == key { count += 1 } END { print count + 0 }' "${file}")"
  if [[ "${match_count}" != "1" ]]; then
    echo "expected exactly one ${key}= entry in ${file}, found ${match_count}" >&2
    exit 1
  fi

  actual="$(awk -F= -v key="${key}" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "${file}")"
  assert_equals "${actual}" "${expected}" "${key}"
}

package_chart() {
  local version="$1"
  local output_dir="$2"

  make helm-package \
    CHART_OUTPUT_DIR="${output_dir}" \
    CHART_VERSION="${version}" \
    CHART_APP_VERSION="${version}" >/dev/null
}

push_chart() {
  local archive="$1"
  local output_file="$2"

  PAGES_GIT_REMOTE_URL="${tmp_dir}/remote.git" \
  PAGES_SITE_URL="${repo_url}" \
  GITHUB_OUTPUT="${output_file}" \
  CHART_ARCHIVE="${archive}" \
    bash ./scripts/ci/publish-gh-pages-chart-repo.sh
}

expect_push_failure() {
  local archive="$1"
  local log_file="$2"
  local output_file="${tmp_dir}/failure-output.txt"

  if push_chart "${archive}" "${output_file}" >"${log_file}" 2>&1; then
    echo "expected chart publish to fail for ${archive}" >&2
    exit 1
  fi
}

clone_branch() {
  local dest_dir="$1"

  git clone --branch gh-pages "${tmp_dir}/remote.git" "${dest_dir}" >/dev/null 2>&1
}

git init --bare "${tmp_dir}/remote.git" >/dev/null

first_version="1.8.2"
first_output_dir="${tmp_dir}/charts-first"
package_chart "${first_version}" "${first_output_dir}"
first_archive="${first_output_dir}/${chart_name}-${first_version}.tgz"
first_output="${tmp_dir}/first-output.txt"
push_chart "${first_archive}" "${first_output}"
assert_output_value "${first_output}" pages_branch gh-pages

first_clone="${tmp_dir}/first-clone"
clone_branch "${first_clone}"
assert_contains "${first_clone}/charts/index.yaml" "version: ${first_version}"
assert_contains "${first_clone}/charts/index.yaml" "${repo_url}/${chart_name}-${first_version}.tgz"
if [[ ! -f "${first_clone}/charts/${chart_name}-${first_version}.tgz" ]]; then
  echo "expected first packaged chart in gh-pages branch" >&2
  exit 1
fi

second_version="1.8.3"
second_output_dir="${tmp_dir}/charts-second"
package_chart "${second_version}" "${second_output_dir}"
second_archive="${second_output_dir}/${chart_name}-${second_version}.tgz"
second_output="${tmp_dir}/second-output.txt"
push_chart "${second_archive}" "${second_output}"
assert_output_value "${second_output}" pages_branch gh-pages

second_clone="${tmp_dir}/second-clone"
clone_branch "${second_clone}"
assert_contains "${second_clone}/charts/index.yaml" "version: ${first_version}"
assert_contains "${second_clone}/charts/index.yaml" "version: ${second_version}"
assert_contains "${second_clone}/charts/index.yaml" "${repo_url}/${chart_name}-${first_version}.tgz"
assert_contains "${second_clone}/charts/index.yaml" "${repo_url}/${chart_name}-${second_version}.tgz"
if [[ ! -f "${second_clone}/charts/${chart_name}-${first_version}.tgz" ]]; then
  echo "expected first chart to remain in gh-pages branch" >&2
  exit 1
fi
if [[ ! -f "${second_clone}/charts/${chart_name}-${second_version}.tgz" ]]; then
  echo "expected second chart in gh-pages branch" >&2
  exit 1
fi

baseline_commit_count="$(git -C "${second_clone}" rev-list --count HEAD)"
baseline_index_hash="$(sha256sum "${second_clone}/charts/index.yaml" | awk '{print $1}')"

rerun_output="${tmp_dir}/rerun-output.txt"
push_chart "${first_archive}" "${rerun_output}"
assert_output_value "${rerun_output}" pages_branch gh-pages

rerun_clone="${tmp_dir}/rerun-clone"
clone_branch "${rerun_clone}"
assert_equals "$(git -C "${rerun_clone}" rev-list --count HEAD)" "${baseline_commit_count}" "rerun commit count"
assert_equals "$(sha256sum "${rerun_clone}/charts/index.yaml" | awk '{print $1}')" "${baseline_index_hash}" "rerun index hash"

mutated_output_dir="${tmp_dir}/charts-mutated"
package_chart "${first_version}" "${mutated_output_dir}"
mutated_archive="${mutated_output_dir}/${chart_name}-${first_version}.tgz"
printf 'mutated chart bytes\n' >> "${mutated_archive}"
failure_log="${tmp_dir}/immutable-failure.log"
expect_push_failure "${mutated_archive}" "${failure_log}"
assert_contains "${failure_log}" "refusing to overwrite immutable stable chart ${chart_name}-${first_version}.tgz in gh-pages"

immutable_clone="${tmp_dir}/immutable-clone"
clone_branch "${immutable_clone}"
assert_equals "$(git -C "${immutable_clone}" rev-list --count HEAD)" "${baseline_commit_count}" "immutable failure commit count"
assert_equals "$(sha256sum "${immutable_clone}/charts/index.yaml" | awk '{print $1}')" "${baseline_index_hash}" "immutable failure index hash"
