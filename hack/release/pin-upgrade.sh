#!/usr/bin/env bash
# hack/release/pin-upgrade.sh X.Y.Z
#
# Prints the hack/e2e/versions.env pins of kardinal-promoter vX.Y.Z as
# published, which its upgrade suite (upgrade-v<XYZ>,
# components/kardinal-v<XYZ>.sh) installs it by:
#
#   V<XYZ>_CHART_DIGEST       the chart's OCI manifest digest,
#                             ghcr.io/pnz1990/charts/kardinal-promoter:X.Y.Z
#   V<XYZ>_CONTROLLER_DIGEST  the controller image's index digest,
#                             ghcr.io/pnz1990/kardinal-promoter/controller:vX.Y.Z
#   V<XYZ>_CLI_SHA256         the sha256 of the release's kardinal-linux-amd64,
#                             downloaded, which must be the digest GitHub
#                             records for the asset
#
# Run it once release.yml has published the release, and put the three lines
# in versions.env (CONTRIBUTING.md, "Pin the upgrade suite"). Read-only: it
# pulls anonymously from ghcr.io and GitHub. The GitHub API request sends
# GH_TOKEN or GITHUB_TOKEN when one is set, else gh's login when gh has one,
# else no token (60 requests an hour). Nothing it reads is printed but the
# pins.
#
# Exit status: 0 printed; 1 a value is missing, malformed or does not check
# (a release not published yet answers 404); 2 usage; 3 a service could not be
# reached, or refused the request (403, 429 or 5xx: rate limited or down), so
# the pins could not be read: try again.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

if [ "$#" -ne 1 ] || ! [[ $1 =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: $0 X.Y.Z" >&2
  exit 2
fi
version=$1
prefix=V${version//./}
repo=pnz1990/kardinal-promoter
asset=kardinal-linux-amd64

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
umask 077

# get URL OUT [CURL_ARG...] writes URL's body to OUT, following redirects.
get() {
  local url=$1 out=$2 code
  shift 2
  if ! code=$(curl -sS -L --retry 3 --max-time 300 -o "$out" -w '%{http_code}' "$@" "$url" 2>"$tmp/curl.err"); then
    echo "can't read $url: $(cat "$tmp/curl.err")" >&2
    exit 3
  fi
  case $code in
    2??) ;;
    403 | 429 | 5??)
      echo "$url answered HTTP $code (rate limited or down): try again" >&2
      exit 3
      ;;
    404)
      echo "$url answered HTTP 404: is v$version published?" >&2
      exit 1
      ;;
    *)
      echo "$url answered HTTP $code" >&2
      exit 1
      ;;
  esac
}

# digest REPOSITORY REFERENCE ACCEPT prints the Docker-Content-Digest ghcr.io
# answers for the manifest, with an anonymous pull token.
digest() {
  get "https://ghcr.io/token?scope=repository:$1:pull" "$tmp/token.json"
  python3 -I -c 'import json,sys; print("Authorization: Bearer " + json.load(open(sys.argv[1]))["token"])' \
    "$tmp/token.json" >"$tmp/ghcr.header"
  get "https://ghcr.io/v2/$1/manifests/$2" /dev/null -I -D "$tmp/headers" -H "@$tmp/ghcr.header" -H "Accept: $3"
  tr -d '\r' <"$tmp/headers" | awk 'tolower($1) == "docker-content-digest:" { d = $2 } END { print d }'
}

# GitHub's record of the CLI asset first: it is the request a rate limit
# refuses, and it is cheap.
token=${GH_TOKEN:-${GITHUB_TOKEN:-}}
if [ -z "$token" ] && command -v gh >/dev/null 2>&1; then
  token=$(gh auth token 2>/dev/null || true)
fi
api=(-H "Accept: application/vnd.github+json")
if [ -n "$token" ]; then
  printf 'Authorization: Bearer %s\n' "$token" >"$tmp/github.header"
  api+=(-H "@$tmp/github.header")
fi
get "https://api.github.com/repos/$repo/releases/tags/v$version" "$tmp/release.json" "${api[@]}"
recorded=$(python3 -I -c 'import json,sys
for a in json.load(open(sys.argv[1])).get("assets", []):
    if a.get("name") == sys.argv[2]:
        print(a.get("digest") or "")' "$tmp/release.json" "$asset")
[[ $recorded =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "GitHub records no sha256 digest for v$version's $asset: '$recorded'" >&2
  exit 1
}

chart=$(digest pnz1990/charts/kardinal-promoter "$version" application/vnd.oci.image.manifest.v1+json)
controller=$(digest "$repo/controller" "v$version" \
  "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json")
for d in "$chart" "$controller"; do
  [[ $d =~ ^sha256:[0-9a-f]{64}$ ]] || {
    echo "ghcr.io answered digest '$d', not sha256:<64 hex>" >&2
    exit 1
  }
done

# The CLI: downloaded into a directory of its own and only hashed.
mkdir "$tmp/cli"
get "https://github.com/$repo/releases/download/v$version/$asset" "$tmp/cli/$asset"
sum=$(sha256sum "$tmp/cli/$asset" | awk '{ print $1 }')
if [ "sha256:$sum" != "$recorded" ]; then
  echo "$asset: the download's sha256 is $sum, GitHub records $recorded for the asset" >&2
  exit 1
fi

echo "${prefix}_CHART_DIGEST=$chart"
echo "${prefix}_CONTROLLER_DIGEST=$controller"
echo "${prefix}_CLI_SHA256=$sum"
