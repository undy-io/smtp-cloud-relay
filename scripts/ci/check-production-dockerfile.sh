#!/usr/bin/env bash

set -euo pipefail

dockerfile_path="${1:-Dockerfile}"

mapfile -t from_lines < <(grep -nE '^FROM ' "${dockerfile_path}")
if [[ "${#from_lines[@]}" -eq 0 ]]; then
  echo "expected at least one FROM line in ${dockerfile_path}" >&2
  exit 1
fi

for entry in "${from_lines[@]}"; do
  line_number="${entry%%:*}"
  line_content="${entry#*:}"

  if [[ ! "${line_content}" =~ @sha256:[0-9a-f]{64}([[:space:]]|$) ]]; then
    echo "${dockerfile_path}:${line_number} must pin the base image with an @sha256: digest" >&2
    exit 1
  fi
done
