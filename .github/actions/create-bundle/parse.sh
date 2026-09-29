#!/usr/bin/env bash
# parse.sh — image parsing and request-body building for the create-bundle action.
# Sourced by create-bundle.sh and test.sh, so the tests exercise the code the
# action runs. Values are passed to python3 as arguments or environment
# variables and are never placed inside program text.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# json_object key value [key value ...] — prints a compact JSON object.
json_object() {
  python3 -c 'import json, sys
a = sys.argv[1:]
print(json.dumps(dict(zip(a[::2], a[1::2])), separators=(",", ":")))' "$@"
}

# parse_image <ref> [override_digest] — prints one ImageRef JSON object.
# A ':' in the last path segment is a tag; a ':' before a '/' is a registry port.
# override_digest replaces any tag or digest in the ref.
parse_image() {
  local img="$1" override_digest="${2:-}" repo digest="" tag="" last
  repo="$img"
  if [[ "$repo" == *@* ]]; then
    digest="${repo#*@}"
    repo="${repo%%@*}"
  fi
  last="${repo##*/}"
  if [[ "$last" == *:* ]]; then
    tag="${repo##*:}"
    repo="${repo%:*}"
  fi
  if [ -n "$override_digest" ]; then
    digest="$override_digest"
  fi
  if [ -n "$digest" ]; then
    json_object repository "$repo" digest "$digest"
  elif [ -n "$tag" ]; then
    json_object repository "$repo" tag "$tag"
  else
    json_object repository "$repo"
  fi
}

# build_images_json <image> <digest> <images> — prints the images JSON array.
# image (single) takes precedence over images (newline-separated list).
build_images_json() {
  local image="$1" digest="$2" images="$3" items="" entry img
  if [ -n "$image" ]; then
    items=$(parse_image "$image" "$digest")
  elif [ -n "$images" ]; then
    while IFS= read -r img; do
      img="${img#"${img%%[![:space:]]*}"}"
      img="${img%"${img##*[![:space:]]}"}"
      [ -z "$img" ] && continue
      entry=$(parse_image "$img" "")
      items="${items:+$items,}$entry"
    done <<<"$images"
  fi
  echo "[$items]"
}

# build_body <pipeline> <type> <namespace> <images_json> — prints the
# POST /api/v1/bundles request body, with provenance from the GitHub Actions
# environment.
build_body() {
  python3 -c 'import json, os, sys
pipeline, bundle_type, namespace, images = sys.argv[1:5]
print(json.dumps({
    "pipeline": pipeline,
    "type": bundle_type,
    "namespace": namespace,
    "images": json.loads(images),
    "provenance": {
        "commitSHA": os.environ.get("GITHUB_SHA", ""),
        "ciRunURL": "{}/{}/actions/runs/{}".format(
            os.environ.get("GITHUB_SERVER_URL", "https://github.com"),
            os.environ.get("GITHUB_REPOSITORY", "unknown"),
            os.environ.get("GITHUB_RUN_ID", "0")),
        "author": os.environ.get("GITHUB_ACTOR", ""),
    },
}))' "$@"
}

# is_k8s_name <value> — true when value is a valid Kubernetes object name
# (DNS-1123 subdomain). Rejects newlines, so a value cannot add lines to
# $GITHUB_OUTPUT.
is_k8s_name() {
  [[ "$1" =~ ^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$ ]]
}

# json_field <field> — reads a JSON object on stdin and prints one string field.
json_field() {
  python3 -c 'import json, sys
print(json.load(sys.stdin)[sys.argv[1]])' "$1"
}

# status_url <ui_url> <pipeline> — prints the UI link for a pipeline, or
# nothing when no UI URL is configured.
status_url() {
  local ui="${1%/}" pipeline="$2"
  [ -n "$ui" ] || return 0
  echo "${ui}/ui/#pipeline=${pipeline}"
}
