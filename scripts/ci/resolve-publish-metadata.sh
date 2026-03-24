#!/usr/bin/env bash

set -euo pipefail

: "${CHART_DIR:?CHART_DIR is required}"
: "${REGISTRY:?REGISTRY is required}"
: "${IMAGE_NAME:?IMAGE_NAME is required}"
: "${GITHUB_REF:?GITHUB_REF is required}"
: "${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"
: "${GITHUB_RUN_NUMBER:?GITHUB_RUN_NUMBER is required}"
: "${GITHUB_RUN_ATTEMPT:?GITHUB_RUN_ATTEMPT is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"

chart_name="$(awk '$1 == "name:" { print $2; exit }' "${CHART_DIR}/Chart.yaml")"
chart_version="$(awk '$1 == "version:" { print $2; exit }' "${CHART_DIR}/Chart.yaml")"

if [[ -z "${chart_name}" || -z "${chart_version}" ]]; then
  echo "failed to read chart metadata from ${CHART_DIR}/Chart.yaml" >&2
  exit 1
fi

if [[ ! "${chart_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Chart.yaml version must use the X.Y.Z form" >&2
  exit 1
fi

short_sha="${GITHUB_SHA::12}"
stable_sha_tag="sha-${short_sha}"
nightly_sha_tag="nightly-sha-${short_sha}"
git_remote_name="${PUBLISH_GIT_REMOTE:-origin}"
git_main_branch="${PUBLISH_MAIN_BRANCH:-master}"
stable_blocker_source_ref="${git_remote_name}/${git_main_branch}"
image_repository="${REGISTRY}/${IMAGE_NAME}"
image_minor_alias_ref=""
image_major_alias_ref=""
image_latest_alias_ref=""
image_minor_blocker_refs=""
image_major_blocker_refs=""
image_latest_blocker_refs=""

version_gt() {
  local candidate="$1"
  local current="$2"

  [[ "${candidate}" != "${current}" && "$(printf '%s\n%s\n' "${current}" "${candidate}" | sort -V | tail -n1)" == "${candidate}" ]]
}

format_ref_lines() {
  local -n in_refs_ref="$1"
  local out_name="$2"
  local formatted_value=""

  if [[ "${#in_refs_ref[@]}" -gt 0 ]]; then
    printf -v formatted_value '%s\n' "${in_refs_ref[@]}"
  fi

  printf -v "${out_name}" '%s' "${formatted_value}"
}

reachable_release_tags() {
  local blocker_source_commit=""
  local tag_list=""

  blocker_source_commit="$(git rev-parse --verify --quiet "${stable_blocker_source_ref}^{commit}")" || {
    echo "failed to resolve stable blocker source ref ${stable_blocker_source_ref}" >&2
    return 1
  }

  tag_list="$(git tag --merged "${blocker_source_commit}")" || {
    echo "failed to enumerate stable release tags merged into ${stable_blocker_source_ref}" >&2
    return 1
  }

  awk '/^[0-9]+\.[0-9]+\.[0-9]+$/' <<< "${tag_list}" | sort -V
}

if [[ "${GITHUB_REF}" == refs/tags/* ]]; then
  publish_kind="stable"
  release_version="${GITHUB_REF_NAME}"
  if [[ ! "${release_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "release tags must use the X.Y.Z form" >&2
    exit 1
  fi
  if [[ "${release_version}" != "${chart_version}" ]]; then
    echo "tag version ${release_version} does not match Chart.yaml version ${chart_version}" >&2
    exit 1
  fi

  IFS=. read -r major minor _ <<< "${release_version}"
  major_minor="${major}.${minor}"
  declare -a minor_blockers=()
  declare -a major_blockers=()
  declare -a latest_blockers=()
  reachable_tags_output=""
  reachable_tag=""

  publish_chart_version="${release_version}"
  publish_app_version="${release_version}"
  image_canonical_ref="${image_repository}:${release_version}"
  image_sha_ref="${image_repository}:${stable_sha_tag}"
  image_promote_ref="${image_repository}:${nightly_sha_tag}"
  image_minor_alias_ref="${image_repository}:${major_minor}"
  image_major_alias_ref="${image_repository}:${major}"
  image_latest_alias_ref="${image_repository}:latest"
  printf -v image_build_refs '%s\n%s' \
    "${image_canonical_ref}" \
    "${image_sha_ref}"

  reachable_tags_output="$(reachable_release_tags)"
  if [[ -n "${reachable_tags_output}" ]]; then
    while IFS= read -r reachable_tag; do
      local_major=""
      local_minor=""
      if [[ -z "${reachable_tag}" ]] || ! version_gt "${reachable_tag}" "${release_version}"; then
        continue
      fi

      latest_blockers+=("${image_repository}:${reachable_tag}")
      IFS=. read -r local_major local_minor _ <<< "${reachable_tag}"
      if [[ "${local_major}" == "${major}" ]]; then
        major_blockers+=("${image_repository}:${reachable_tag}")
        if [[ "${local_major}.${local_minor}" == "${major_minor}" ]]; then
          minor_blockers+=("${image_repository}:${reachable_tag}")
        fi
      fi
    done <<< "${reachable_tags_output}"
  fi

  format_ref_lines minor_blockers image_minor_blocker_refs
  format_ref_lines major_blockers image_major_blocker_refs
  format_ref_lines latest_blockers image_latest_blocker_refs
elif [[ "${GITHUB_REF}" == "refs/heads/${git_main_branch}" ]]; then
  publish_kind="nightly"
  publish_chart_version="${chart_version}-nightly.${GITHUB_RUN_NUMBER}.${GITHUB_RUN_ATTEMPT}"
  publish_app_version="${publish_chart_version}"
  image_canonical_ref="${image_repository}:${publish_chart_version}"
  image_sha_ref="${image_repository}:${nightly_sha_tag}"
  image_promote_ref="${image_sha_ref}"
  printf -v image_build_refs '%s\n%s\n%s' \
    "${image_canonical_ref}" \
    "${image_repository}:nightly" \
    "${image_sha_ref}"
else
  echo "unsupported ref ${GITHUB_REF}; only refs/heads/${git_main_branch} and refs/tags/X.Y.Z are supported" >&2
  exit 1
fi

{
  echo "publish_kind=${publish_kind}"
  echo "chart_name=${chart_name}"
  echo "chart_version=${publish_chart_version}"
  echo "chart_app_version=${publish_app_version}"
  echo "chart_archive=dist/charts/${chart_name}-${publish_chart_version}.tgz"
  echo "image_repository=${image_repository}"
  echo "image_canonical_ref=${image_canonical_ref}"
  echo "image_sha_ref=${image_sha_ref}"
  echo "image_promote_ref=${image_promote_ref}"
  echo "image_minor_alias_ref=${image_minor_alias_ref}"
  echo "image_major_alias_ref=${image_major_alias_ref}"
  echo "image_latest_alias_ref=${image_latest_alias_ref}"
  echo "image_build_refs<<EOF"
  printf '%s\n' "${image_build_refs}"
  echo "EOF"
  echo "image_minor_blocker_refs<<EOF"
  if [[ -n "${image_minor_blocker_refs}" ]]; then
    printf '%s' "${image_minor_blocker_refs}"
  fi
  echo "EOF"
  echo "image_major_blocker_refs<<EOF"
  if [[ -n "${image_major_blocker_refs}" ]]; then
    printf '%s' "${image_major_blocker_refs}"
  fi
  echo "EOF"
  echo "image_latest_blocker_refs<<EOF"
  if [[ -n "${image_latest_blocker_refs}" ]]; then
    printf '%s' "${image_latest_blocker_refs}"
  fi
  echo "EOF"
} >> "${GITHUB_OUTPUT}"
