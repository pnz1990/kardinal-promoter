#!/usr/bin/env bash
# create-bundle.sh — the create-bundle composite action's step.
#
# Inputs arrive as INPUT_* environment variables (set by action.yml) and are
# only ever expanded as quoted shell variables or passed to python3 as
# arguments, so a hostile input cannot run code in the caller's job.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.github/actions/create-bundle/parse.sh
source "$HERE/parse.sh"

PIPELINE="${INPUT_PIPELINE:-}"
BUNDLE_TYPE="${INPUT_TYPE:-image}"
NAMESPACE="${INPUT_NAMESPACE:-default}"
KARDINAL_URL="${INPUT_KARDINAL_URL:-}"
KARDINAL_URL="${KARDINAL_URL%/}"
UI_URL="${INPUT_UI_URL:-}"
MAX_ATTEMPTS="${CREATE_BUNDLE_MAX_ATTEMPTS:-3}"
SLEEP_SECS="${CREATE_BUNDLE_RETRY_SLEEP_SECS:-1}"
: "${GITHUB_OUTPUT:=/dev/null}"

if [ -z "${KARDINAL_TOKEN:-}" ]; then
  echo "::error::KARDINAL_TOKEN environment variable is not set. Set it as a repository secret and pass it via env: KARDINAL_TOKEN: \${{ secrets.KARDINAL_TOKEN }}"
  exit 1
fi
if ! is_k8s_name "$PIPELINE"; then
  echo "::error::the pipeline input must be a Kubernetes object name (lowercase letters, digits, '-', '.')"
  exit 1
fi
if ! is_k8s_name "$NAMESPACE"; then
  echo "::error::the namespace input must be a Kubernetes namespace name"
  exit 1
fi
if [ -z "$KARDINAL_URL" ]; then
  echo "::error::the kardinal-url input is required"
  exit 1
fi
case "$KARDINAL_URL$UI_URL" in
  *[[:space:]]*)
    echo "::error::kardinal-url and ui-url must not contain whitespace"
    exit 1
    ;;
esac

echo "::group::Build image list"
IMAGES_JSON=$(build_images_json "${INPUT_IMAGE:-}" "${INPUT_DIGEST:-}" "${INPUT_IMAGES:-}")
if [ "$IMAGES_JSON" = "[]" ]; then
  echo "No images provided — creating a bundle with an empty image list."
fi
echo "Images JSON: $IMAGES_JSON"
echo "::endgroup::"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
build_body "$PIPELINE" "$BUNDLE_TYPE" "$NAMESPACE" "$IMAGES_JSON" >"$WORK/body.json"
# The token goes in a header file, not on the curl command line.
(
  umask 077
  printf 'Authorization: Bearer %s\n' "$KARDINAL_TOKEN" >"$WORK/auth.header"
)

echo "::group::Create Bundle"
echo "Pipeline: $PIPELINE"
echo "Namespace: $NAMESPACE"
echo "Endpoint: ${KARDINAL_URL}/api/v1/bundles"

# Creating a Bundle is not idempotent, so only retry when the request cannot
# have reached the API handler: curl could not resolve or connect (exit 6/7),
# or a proxy answered 502/503. Timeouts and other errors are not retried,
# because the Bundle may already exist.
ATTEMPT=0
HTTP_STATUS=""
while :; do
  ATTEMPT=$((ATTEMPT + 1))
  echo "Attempt $ATTEMPT/$MAX_ATTEMPTS..."
  CURL_EXIT=0
  HTTP_STATUS=$(curl -sS -o "$WORK/response.json" -w '%{http_code}' \
    -X POST \
    -H @"$WORK/auth.header" \
    -H "Content-Type: application/json" \
    --data-binary @"$WORK/body.json" \
    --connect-timeout 10 \
    --max-time 30 \
    "${KARDINAL_URL}/api/v1/bundles") || CURL_EXIT=$?

  RETRYABLE=false
  if [ "$CURL_EXIT" -ne 0 ]; then
    echo "curl failed with exit code $CURL_EXIT"
    case "$CURL_EXIT" in 6 | 7) RETRYABLE=true ;; esac
  else
    echo "HTTP status: $HTTP_STATUS"
    case "$HTTP_STATUS" in
      2??) break ;;
      502 | 503) RETRYABLE=true ;;
    esac
  fi

  if [ "$RETRYABLE" = true ] && [ "$ATTEMPT" -lt "$MAX_ATTEMPTS" ]; then
    echo "Retrying in ${SLEEP_SECS}s..."
    sleep "$SLEEP_SECS"
    SLEEP_SECS=$((SLEEP_SECS * 2))
    continue
  fi

  if [ "$CURL_EXIT" -ne 0 ]; then
    echo "::error::Bundle creation failed: curl exit $CURL_EXIT after $ATTEMPT attempt(s)."
  else
    echo "::error::Bundle creation failed with HTTP $HTTP_STATUS after $ATTEMPT attempt(s)."
    echo "::error::Response: $(head -c 2000 "$WORK/response.json" 2>/dev/null)"
  fi
  exit 1
done

BUNDLE_NAME=$(json_field name <"$WORK/response.json" 2>/dev/null || true)
BUNDLE_NS=$(json_field namespace <"$WORK/response.json" 2>/dev/null || true)
if ! is_k8s_name "$BUNDLE_NAME" || ! is_k8s_name "$BUNDLE_NS"; then
  echo "::error::Bundle creation failed — could not parse the bundle name and namespace from the response."
  echo "::error::Response: $(head -c 2000 "$WORK/response.json" 2>/dev/null)"
  exit 1
fi

STATUS_URL=$(status_url "$UI_URL" "$PIPELINE")

echo "Bundle created: $BUNDLE_NAME in namespace $BUNDLE_NS"
if [ -n "$STATUS_URL" ]; then
  echo "Status URL: $STATUS_URL"
else
  echo "Status URL: (set the ui-url input to get a link to the kardinal UI)"
fi
echo "::endgroup::"

{
  echo "bundle-name=$BUNDLE_NAME"
  echo "bundle-namespace=$BUNDLE_NS"
  echo "bundle-status-url=$STATUS_URL"
} >>"$GITHUB_OUTPUT"
