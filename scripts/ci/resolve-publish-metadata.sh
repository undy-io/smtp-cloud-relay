#!/usr/bin/env bash

set -euo pipefail

: "${CHART_DIR:?CHART_DIR is required}"
: "${REGISTRY:?REGISTRY is required}"
: "${IMAGE_NAME:?IMAGE_NAME is required}"
: "${GITHUB_REF:?GITHUB_REF is required}"
: "${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"
: "${GITHUB_RUN_NUMBER:?GITHUB_RUN_NUMBER is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"

chart_name="$(awk '$1 == "name:" { print $2; exit }' "${CHART_DIR}/Chart.yaml")"
chart_version="$(awk '$1 == "version:" { print $2; exit }' "${CHART_DIR}/Chart.yaml")"

if [[ -z "${chart_name}" || -z "${chart_version}" ]]; then
  echo "failed to read chart metadata from ${CHART_DIR}/Chart.yaml" >&2
  exit 1
fi

short_sha="${GITHUB_SHA::12}"
sha_tag="sha-${short_sha}"

if [[ "${GITHUB_REF}" == refs/tags/* ]]; then
  release_version="${GITHUB_REF_NAME#v}"
  if [[ "${GITHUB_REF_NAME}" != v* || -z "${release_version}" ]]; then
    echo "release tags must use the vX.Y.Z form" >&2
    exit 1
  fi
  if [[ "${release_version}" != "${chart_version}" ]]; then
    echo "tag version ${release_version} does not match Chart.yaml version ${chart_version}" >&2
    exit 1
  fi

  publish_chart_version="${release_version}"
  publish_app_version="${release_version}"
  printf -v image_tags '%s\n%s' \
    "${REGISTRY}/${IMAGE_NAME}:${release_version}" \
    "${REGISTRY}/${IMAGE_NAME}:${sha_tag}"
else
  publish_chart_version="${chart_version}-main.${GITHUB_RUN_NUMBER}"
  publish_app_version="${sha_tag}"
  printf -v image_tags '%s\n%s' \
    "${REGISTRY}/${IMAGE_NAME}:main" \
    "${REGISTRY}/${IMAGE_NAME}:${sha_tag}"
fi

{
  echo "chart_version=${publish_chart_version}"
  echo "chart_app_version=${publish_app_version}"
  echo "chart_archive=dist/charts/${chart_name}-${publish_chart_version}.tgz"
  echo "image_tags<<EOF"
  printf '%s\n' "${image_tags}"
  echo "EOF"
} >> "${GITHUB_OUTPUT}"
