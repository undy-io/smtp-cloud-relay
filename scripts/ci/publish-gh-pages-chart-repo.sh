#!/usr/bin/env bash

set -euo pipefail

: "${CHART_ARCHIVE:?CHART_ARCHIVE is required}"
: "${PAGES_SITE_URL:?PAGES_SITE_URL is required}"

pages_branch="${PAGES_BRANCH:-gh-pages}"
repo_url="${PAGES_GIT_REMOTE_URL:-}"
chart_basename="$(basename "${CHART_ARCHIVE}")"

if [[ -z "${repo_url}" ]]; then
  : "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required when PAGES_GIT_REMOTE_URL is unset}"
  : "${GITHUB_TOKEN:?GITHUB_TOKEN is required when PAGES_GIT_REMOTE_URL is unset}"
  repo_url="https://x-access-token:${GITHUB_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"
fi

if [[ ! -f "${CHART_ARCHIVE}" ]]; then
  echo "packaged chart not found: ${CHART_ARCHIVE}" >&2
  exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

repo_dir="${work_dir}/repo"
if git clone --branch "${pages_branch}" --depth 1 "${repo_url}" "${repo_dir}" >/dev/null 2>&1; then
  :
else
  git init "${repo_dir}" >/dev/null
  (
    cd "${repo_dir}"
    git checkout --orphan "${pages_branch}" >/dev/null
    git remote add origin "${repo_url}"
  )
fi

update_pages_repo() {
  cd "${repo_dir}"
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"

  mkdir -p charts
  target_chart="charts/${chart_basename}"
  if [[ -f "${target_chart}" ]]; then
    if cmp -s "${CHART_ARCHIVE}" "${target_chart}"; then
      return 0
    fi

    echo "refusing to overwrite immutable stable chart ${chart_basename} in ${pages_branch}" >&2
    return 1
  fi

  cp "${CHART_ARCHIVE}" "${target_chart}"
  touch .nojekyll

  if [[ -f charts/index.yaml ]]; then
    helm repo index charts --url "${PAGES_SITE_URL}" --merge charts/index.yaml >/dev/null
  else
    helm repo index charts --url "${PAGES_SITE_URL}" >/dev/null
  fi

  git add charts .nojekyll
  if ! git diff --cached --quiet; then
    git commit -m "Publish Helm chart ${chart_basename}" >/dev/null
    git push origin HEAD:"${pages_branch}" >/dev/null
  fi
}

update_pages_repo

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "pages_branch=${pages_branch}" >> "${GITHUB_OUTPUT}"
fi
