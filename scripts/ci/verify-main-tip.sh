#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REF:?GITHUB_REF is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"

git_remote_name="${PUBLISH_GIT_REMOTE:-origin}"
git_main_branch="${PUBLISH_MAIN_BRANCH:-master}"

case "${GITHUB_REF}" in
  "refs/heads/${git_main_branch}")
    git fetch "${git_remote_name}" "${git_main_branch}" >/dev/null
    current_main_sha="$(git rev-parse FETCH_HEAD)"
    if [[ "${current_main_sha}" != "${GITHUB_SHA}" ]]; then
      echo "refusing to publish ${GITHUB_REF} at ${GITHUB_SHA}; current ${git_main_branch} tip is ${current_main_sha}" >&2
      exit 1
    fi
    ;;
  refs/tags/*)
    if [[ ! "${GITHUB_REF#refs/tags/}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      exit 0
    fi
    git fetch "${git_remote_name}" "${git_main_branch}" >/dev/null
    if ! git merge-base --is-ancestor "${GITHUB_SHA}" FETCH_HEAD; then
      echo "refusing to publish ${GITHUB_REF} at ${GITHUB_SHA}; commit is not reachable from ${git_remote_name}/${git_main_branch}" >&2
      exit 1
    fi
    ;;
  *)
    exit 0
    ;;
esac
