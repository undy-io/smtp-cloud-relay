#!/usr/bin/env bash

set -euo pipefail

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
verify_main_tip="${script_dir}/verify-main-tip.sh"

if [[ ! -f "${verify_main_tip}" || ! -r "${verify_main_tip}" ]]; then
  echo "missing readable helper: ${verify_main_tip}" >&2
  exit 1
fi

verify_main_tip_copy="${tmp_dir}/verify-main-tip.sh"
cp "${verify_main_tip}" "${verify_main_tip_copy}"
chmod 0644 "${verify_main_tip_copy}"

assert_contains() {
  local file="$1"
  local pattern="$2"

  if ! grep -Fq -- "${pattern}" "${file}"; then
    echo "expected ${file} to contain: ${pattern}" >&2
    exit 1
  fi
}

remote_dir="${tmp_dir}/remote.git"
work_dir="${tmp_dir}/work"

git init --bare "${remote_dir}" >/dev/null
git clone "${remote_dir}" "${work_dir}" >/dev/null 2>&1

(
  cd "${work_dir}"
  git config user.name "CI Test"
  git config user.email "ci@example.com"
  git checkout -b main >/dev/null

  printf 'first\n' > tracked.txt
  git add tracked.txt
  git commit -m "first" >/dev/null
  git push origin HEAD:main >/dev/null
  stale_sha="$(git rev-parse HEAD)"

  printf 'second\n' >> tracked.txt
  git add tracked.txt
  git commit -m "second" >/dev/null
  git push origin HEAD:main >/dev/null
  current_sha="$(git rev-parse HEAD)"

  GITHUB_REF=refs/heads/main \
  GITHUB_SHA="${current_sha}" \
    bash "${verify_main_tip_copy}"

  stale_log="${tmp_dir}/stale-main.log"
  if GITHUB_REF=refs/heads/main \
    GITHUB_SHA="${stale_sha}" \
      bash "${verify_main_tip_copy}" >"${stale_log}" 2>&1; then
    echo "expected stale main guard to fail" >&2
    exit 1
  fi

  assert_contains "${stale_log}" "refusing to publish stale main commit"

  GITHUB_REF=refs/tags/1.8.2 \
  GITHUB_SHA="${stale_sha}" \
    bash "${verify_main_tip_copy}"
)
