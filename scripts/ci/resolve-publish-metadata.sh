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
sha_tag="sha-${short_sha}"
image_repository="${REGISTRY}/${IMAGE_NAME}"

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

  publish_chart_version="${release_version}"
  publish_app_version="${release_version}"
  image_canonical_ref="${image_repository}:${release_version}"
  image_sha_ref="${image_repository}:${sha_tag}"
  printf -v image_build_refs '%s\n%s' \
    "${image_canonical_ref}" \
    "${image_sha_ref}"
  printf -v image_alias_refs '%s\n%s\n%s' \
    "${image_repository}:${major_minor}" \
    "${image_repository}:${major}" \
    "${image_repository}:latest"
elif [[ "${GITHUB_REF}" == refs/heads/main ]]; then
  publish_kind="nightly"
  publish_chart_version="${chart_version}-nightly.${GITHUB_RUN_NUMBER}.${GITHUB_RUN_ATTEMPT}"
  publish_app_version="${publish_chart_version}"
  image_canonical_ref="${image_repository}:${publish_chart_version}"
  image_sha_ref="${image_repository}:${sha_tag}"
  printf -v image_build_refs '%s\n%s\n%s' \
    "${image_canonical_ref}" \
    "${image_repository}:nightly" \
    "${image_sha_ref}"
  image_alias_refs=""
else
  echo "unsupported ref ${GITHUB_REF}; only refs/heads/main and refs/tags/X.Y.Z are supported" >&2
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
  echo "image_build_refs<<EOF"
  printf '%s\n' "${image_build_refs}"
  echo "EOF"
  echo "image_alias_refs<<EOF"
  if [[ -n "${image_alias_refs}" ]]; then
    printf '%s\n' "${image_alias_refs}"
  fi
  echo "EOF"
} >> "${GITHUB_OUTPUT}"
