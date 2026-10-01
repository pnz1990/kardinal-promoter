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
# A ':' in the last path segment is a tag; a ':' before a '/' is a registry port;
# '@' starts a digest. A tag and a digest are both sent: the Bundle records an
# image with both, as `kardinal create bundle` does for repo:tag@digest.
# override_digest replaces a digest in the ref; the ref's tag is kept.
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
  local fields=(repository "$repo")
  if [ -n "$tag" ]; then
    fields+=(tag "$tag")
  fi
  if [ -n "$digest" ]; then
    fields+=(digest "$digest")
  fi
  json_object "${fields[@]}"
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

# build_body <pipeline> <type> <namespace> <images_json> [config_commit] [config_repo]
# — prints the POST /api/v1/bundles request body, with provenance from the
# GitHub Actions environment. A config commit adds configRef (config and
# mixed Bundles need one); config_repo is its gitRepo, omitted when empty so
# the controller uses the Pipeline repository.
build_body() {
  python3 -c 'import json, os, sys
pipeline, bundle_type, namespace, images = sys.argv[1:5]
config_commit = sys.argv[5] if len(sys.argv) > 5 else ""
config_repo = sys.argv[6] if len(sys.argv) > 6 else ""
body = {
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
}
if config_commit:
    body["configRef"] = {"commitSHA": config_commit}
    if config_repo:
        body["configRef"]["gitRepo"] = config_repo
print(json.dumps(body))' "$@"
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
