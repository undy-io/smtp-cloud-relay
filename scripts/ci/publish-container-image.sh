#!/usr/bin/env bash

set -euo pipefail

: "${PUBLISH_KIND:?PUBLISH_KIND is required}"
: "${IMAGE_BUILD_REFS:?IMAGE_BUILD_REFS is required}"
: "${IMAGE_CANONICAL_REF:?IMAGE_CANONICAL_REF is required}"
: "${IMAGE_SHA_REF:?IMAGE_SHA_REF is required}"

docker_build_context="${DOCKER_BUILD_CONTEXT:-.}"
registry_probe_retries="${REGISTRY_PROBE_RETRIES:-5}"
registry_probe_delay_seconds="${REGISTRY_PROBE_DELAY_SECONDS:-2}"

read_refs() {
  local value="$1"
  local -n out_ref="$2"
  local line

  out_ref=()
  while IFS= read -r line; do
    if [[ -n "${line}" ]]; then
      out_ref+=("${line}")
    fi
  done <<< "${value}"
}

is_missing_probe_error() {
  local error_output="$1"

  grep -Eiq 'not found|no such manifest|manifest unknown|name unknown|does not exist' <<< "${error_output}"
}

probe_remote_digest() {
  local ref="$1"
  local -n out_state="$2"
  local -n out_digest="$3"
  local attempt digest inspect_output inspect_error

  out_state="error"
  out_digest=""

  for (( attempt = 1; attempt <= registry_probe_retries; attempt += 1 )); do
    inspect_error=""
    if inspect_output="$(docker buildx imagetools inspect "${ref}" 2>&1)"; then
      digest="$(awk '/^Digest:/ { print $2; exit }' <<< "${inspect_output}")"
      if [[ -n "${digest}" ]]; then
        out_state="present"
        out_digest="${digest}"
        return 0
      fi

      echo "docker buildx imagetools inspect succeeded without a digest for ${ref}" >&2
      return 1
    fi

    inspect_error="${inspect_output}"
    if is_missing_probe_error "${inspect_error}"; then
      # shellcheck disable=SC2034
      out_state="missing"
      # shellcheck disable=SC2034
      out_digest=""
      return 0
    fi

    if (( attempt < registry_probe_retries )); then
      sleep "${registry_probe_delay_seconds}"
    fi
  done

  echo "docker buildx imagetools inspect failed for ${ref}: ${inspect_error}" >&2
  return 1
}

build_and_push() {
  local refs=("$@")
  local command=(
    docker buildx build
    --push
    --provenance=false
    --sbom=false
  )
  local ref

  for ref in "${refs[@]}"; do
    command+=(--tag "${ref}")
  done

  if [[ -n "${GITHUB_REPOSITORY:-}" ]]; then
    command+=(--label "org.opencontainers.image.source=https://github.com/${GITHUB_REPOSITORY}")
  fi
  if [[ -n "${GITHUB_SHA:-}" ]]; then
    command+=(--label "org.opencontainers.image.revision=${GITHUB_SHA}")
  fi

  command+=("${docker_build_context}")
  "${command[@]}"
}

create_aliases() {
  local source_ref="$1"
  shift
  local alias_ref

  for alias_ref in "$@"; do
    docker buildx imagetools create --tag "${alias_ref}" "${source_ref}" >/dev/null
  done
}

declare -a image_build_refs=()
declare -a image_alias_refs=()
canonical_state=""
canonical_digest=""
sha_state=""
sha_digest=""

read_refs "${IMAGE_BUILD_REFS}" image_build_refs
read_refs "${IMAGE_ALIAS_REFS:-}" image_alias_refs

if [[ "${#image_build_refs[@]}" -eq 0 ]]; then
  echo "at least one image build ref is required" >&2
  exit 1
fi

case "${PUBLISH_KIND}" in
  nightly)
    build_and_push "${image_build_refs[@]}"
    ;;
  stable)
    probe_remote_digest "${IMAGE_CANONICAL_REF}" canonical_state canonical_digest
    probe_remote_digest "${IMAGE_SHA_REF}" sha_state sha_digest

    if [[ "${canonical_state}" == "present" || "${sha_state}" == "present" ]]; then
      if [[ "${canonical_state}" != "present" || "${sha_state}" != "present" || "${canonical_digest}" != "${sha_digest}" ]]; then
        echo "stable image state is inconsistent for ${IMAGE_CANONICAL_REF} and ${IMAGE_SHA_REF}" >&2
        exit 1
      fi
    elif [[ "${canonical_state}" == "missing" && "${sha_state}" == "missing" ]]; then
      build_and_push "${image_build_refs[@]}"
      probe_remote_digest "${IMAGE_CANONICAL_REF}" canonical_state canonical_digest
      probe_remote_digest "${IMAGE_SHA_REF}" sha_state sha_digest

      if [[ "${canonical_state}" != "present" || "${sha_state}" != "present" || "${canonical_digest}" != "${sha_digest}" ]]; then
        echo "stable image publish did not produce matching digests for ${IMAGE_CANONICAL_REF} and ${IMAGE_SHA_REF}" >&2
        exit 1
      fi
    else
      echo "stable image state is inconsistent for ${IMAGE_CANONICAL_REF} and ${IMAGE_SHA_REF}" >&2
      exit 1
    fi

    if [[ "${#image_alias_refs[@]}" -gt 0 ]]; then
      create_aliases "${IMAGE_CANONICAL_REF}" "${image_alias_refs[@]}"
    fi
    ;;
  *)
    echo "unsupported publish kind ${PUBLISH_KIND}" >&2
    exit 1
    ;;
esac
