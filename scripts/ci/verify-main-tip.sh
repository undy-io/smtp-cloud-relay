#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REF:?GITHUB_REF is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"

git_remote_name="${PUBLISH_GIT_REMOTE:-origin}"
git_main_branch="${PUBLISH_MAIN_BRANCH:-main}"

if [[ "${GITHUB_REF}" != "refs/heads/${git_main_branch}" ]]; then
  exit 0
fi

git fetch --depth 1 "${git_remote_name}" "${git_main_branch}" >/dev/null
current_main_sha="$(git rev-parse FETCH_HEAD)"

if [[ "${current_main_sha}" != "${GITHUB_SHA}" ]]; then
  echo "refusing to publish stale ${git_main_branch} commit ${GITHUB_SHA}; current ${git_main_branch} tip is ${current_main_sha}" >&2
  exit 1
fi
