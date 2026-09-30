#!/usr/bin/env bash
# test.sh — tests for the create-bundle action. Sources parse.sh, the same code
# the action runs, and runs create-bundle.sh end to end against a fake curl.
# Needs no network access. Exits 0 on pass, non-zero on fail.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.github/actions/create-bundle/parse.sh
source "$HERE/parse.sh"

PASS=0
FAIL=0

check() {
  local desc="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    PASS=$((PASS + 1))
    echo "  PASS: $desc"
  else
    FAIL=$((FAIL + 1))
    echo "  FAIL: $desc"
    echo "        got:  $got"
    echo "        want: $want"
  fi
}

echo ""
echo "--- Image parsing ---"
check "repo:tag" "$(parse_image "ghcr.io/myorg/app:v1.2.3")" \
  '{"repository":"ghcr.io/myorg/app","tag":"v1.2.3"}'
check "repo@digest" "$(parse_image "ghcr.io/myorg/app@sha256:abcdef0123456789")" \
  '{"repository":"ghcr.io/myorg/app","digest":"sha256:abcdef0123456789"}'
check "bare repo" "$(parse_image "ghcr.io/myorg/app")" \
  '{"repository":"ghcr.io/myorg/app"}'
check "image + digest override" "$(parse_image "ghcr.io/myorg/app:v1.2.3" "sha256:abc123")" \
  '{"repository":"ghcr.io/myorg/app","digest":"sha256:abc123"}'
check "registry port, tag" "$(parse_image "registry.local:5000/team/app:v1")" \
  '{"repository":"registry.local:5000/team/app","tag":"v1"}'
check "registry port, digest override" "$(parse_image "registry.local:5000/team/app:v1" "sha256:abc")" \
  '{"repository":"registry.local:5000/team/app","digest":"sha256:abc"}'
check "registry port, no tag" "$(parse_image "registry.local:5000/team/app")" \
  '{"repository":"registry.local:5000/team/app"}'
check "tag and digest" "$(parse_image "ghcr.io/myorg/app:v1@sha256:abc")" \
  '{"repository":"ghcr.io/myorg/app","digest":"sha256:abc"}'
check "quotes are JSON-escaped" "$(parse_image 'ghcr.io/a"b:v1')" \
  '{"repository":"ghcr.io/a\"b","tag":"v1"}'

echo ""
echo "--- Image list ---"
check "multi-image list, whitespace trimmed" \
  "$(build_images_json "" "" "  ghcr.io/myorg/app:v1.2.3
ghcr.io/myorg/sidecar@sha256:deadbeef  

")" \
  '[{"repository":"ghcr.io/myorg/app","tag":"v1.2.3"},{"repository":"ghcr.io/myorg/sidecar","digest":"sha256:deadbeef"}]'
check "image wins over images" "$(build_images_json "ghcr.io/a:v1" "" "ghcr.io/b:v2")" \
  '[{"repository":"ghcr.io/a","tag":"v1"}]'
check "no images" "$(build_images_json "" "" "")" "[]"

echo ""
echo "--- Request body ---"
BODY=$(GITHUB_SHA="abc123" GITHUB_SERVER_URL="https://github.com" GITHUB_REPOSITORY="myorg/myapp" \
  GITHUB_RUN_ID="42" GITHUB_ACTOR="engineer" \
  build_body "my-app" "image" "default" '[{"repository":"ghcr.io/a","tag":"v1"}]')
check "body fields" "$(printf '%s' "$BODY" | python3 -c 'import json, sys
b = json.load(sys.stdin)
print(b["pipeline"], b["type"], b["namespace"], b["images"][0]["tag"], b["provenance"]["commitSHA"],
      b["provenance"]["ciRunURL"], b["provenance"]["author"])')" \
  "my-app image default v1 abc123 https://github.com/myorg/myapp/actions/runs/42 engineer"
check "no configRef without a config commit" "$(printf '%s' "$BODY" | python3 -c 'import json, sys
print("configRef" in json.load(sys.stdin))')" "False"
BODY=$(build_body "my-app" "config" "default" '[]' "def456" "https://git.example.com/org/config")
check "config commit and repo set configRef" "$(printf '%s' "$BODY" | python3 -c 'import json, sys
b = json.load(sys.stdin)
print(b["type"], b["configRef"]["commitSHA"], b["configRef"]["gitRepo"], b["images"])')" \
  "config def456 https://git.example.com/org/config []"
BODY=$(build_body "my-app" "mixed" "default" '[{"repository":"ghcr.io/a","tag":"v1"}]' "def456" "")
check "config repo left out when empty" "$(printf '%s' "$BODY" | python3 -c 'import json, sys
print(json.load(sys.stdin)["configRef"])')" "{'commitSHA': 'def456'}"

echo ""
echo "--- Names and URLs ---"
for name in my-app a app.v2; do
  if is_k8s_name "$name"; then check "valid name $name" ok ok; else check "valid name $name" rejected ok; fi
done
for name in "" My-App "-app" "app-" "a'b" 'a$(id)' "$(printf 'a\nb')"; do
  if is_k8s_name "$name"; then check "invalid name ${name@Q}" accepted rejected; else check "invalid name ${name@Q}" rejected rejected; fi
done
check "status url" "$(status_url "https://kardinal.example.com:8082/" my-app)" \
  "https://kardinal.example.com:8082/ui/#pipeline=my-app"
check "no status url without ui-url" "$(status_url "" my-app)" ""

echo ""
echo "--- create-bundle.sh against a fake curl ---"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
# The fake curl answers with $FAKE_CODES (one HTTP code per call, or curl-exit:N),
# copies the request body to $WORK/sent.json and counts calls.
cat >"$WORK/bin/curl" <<'CURL'
#!/usr/bin/env bash
n=$(($(cat "$FAKE_DIR/calls" 2>/dev/null || echo 0) + 1))
echo "$n" >"$FAKE_DIR/calls"
out="" body=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    --data-binary) body="${2#@}"; shift ;;
    -H) case "$2" in @*) cp "${2#@}" "$FAKE_DIR/header" ;; esac; shift ;;
  esac
  shift
done
cp "$body" "$FAKE_DIR/sent.json"
IFS=' ' read -r -a codes <<<"$FAKE_CODES"
code="${codes[$((n - 1))]:-${codes[-1]}}"
case "$code" in curl-exit:*) exit "${code#curl-exit:}" ;; esac
echo '{"name":"my-app-abc12","namespace":"default"}' >"$out"
printf '%s' "$code"
CURL
chmod +x "$WORK/bin/curl"

run_action() {
  rm -f "$WORK/calls" "$WORK/sent.json" "$WORK/header" "$WORK/output"
  : >"$WORK/output"
  env PATH="$WORK/bin:$PATH" FAKE_DIR="$WORK" FAKE_CODES="$1" GITHUB_OUTPUT="$WORK/output" \
    CREATE_BUNDLE_RETRY_SLEEP_SECS=0 KARDINAL_TOKEN=test-token \
    INPUT_TYPE="${RUN_TYPE:-image}" INPUT_CONFIG_COMMIT="${RUN_CONFIG_COMMIT:-}" \
    INPUT_PIPELINE="${2:-my-app}" INPUT_IMAGE="${3:-ghcr.io/myorg/app:v1}" \
    INPUT_KARDINAL_URL="https://kardinal.example.com/" INPUT_UI_URL="https://ui.example.com" \
    bash "$HERE/create-bundle.sh" >"$WORK/log" 2>&1
}
calls() { cat "$WORK/calls" 2>/dev/null || echo 0; }

if run_action 201; then check "201 succeeds" ok ok; else check "201 succeeds" "failed: $(tail -3 "$WORK/log")" ok; fi
check "outputs written" "$(cat "$WORK/output")" "bundle-name=my-app-abc12
bundle-namespace=default
bundle-status-url=https://ui.example.com/ui/#pipeline=my-app"
check "token sent in a header file" "$(cat "$WORK/header")" "Authorization: Bearer test-token"
if grep -q test-token "$WORK/log"; then check "token not logged" logged "not logged"; else check "token not logged" "not logged" "not logged"; fi

# shellcheck disable=SC2016 # the $(...) is the literal payload under test
INJECT='ghcr.io/myorg/app:v1"$(touch '"$WORK"'/pwned)'"'"
run_action 201 my-app "$INJECT" || true
check "hostile image is sent literally" \
  "$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["images"][0]["repository"])' "$WORK/sent.json")" \
  "$INJECT"
check "hostile image runs nothing" "$([ -e "$WORK/pwned" ] && echo ran || echo clean)" clean

if run_action 201 'my-app$(id)'; then check "hostile pipeline rejected" accepted rejected; else check "hostile pipeline rejected" rejected rejected; fi
check "hostile pipeline makes no request" "$(calls)" 0

if RUN_TYPE=config run_action 201; then check "config without config-commit rejected" accepted rejected; else check "config without config-commit rejected" rejected rejected; fi
check "config without config-commit makes no request" "$(calls)" 0
if RUN_TYPE=config RUN_CONFIG_COMMIT=def456 run_action 201; then check "config with config-commit succeeds" ok ok; else check "config with config-commit succeeds" failed ok; fi
check "config Bundle sends configRef" \
  "$(python3 -c 'import json, sys; b = json.load(open(sys.argv[1])); print(b["type"], b["configRef"]["commitSHA"])' "$WORK/sent.json")" \
  "config def456"

if run_action "503 201"; then check "503 then 201 succeeds" ok ok; else check "503 then 201 succeeds" failed ok; fi
check "503 retried once" "$(calls)" 2
if run_action "curl-exit:7 201"; then check "connect failure then 201" ok ok; else check "connect failure then 201" failed ok; fi
if run_action 500; then check "500 fails" ok failed; else check "500 fails" failed failed; fi
check "500 not retried" "$(calls)" 1
if run_action curl-exit:28; then check "timeout fails" ok failed; else check "timeout fails" failed failed; fi
check "timeout not retried" "$(calls)" 1
if run_action 503; then check "503 forever fails" ok failed; else check "503 forever fails" failed failed; fi
check "503 gives up after 3 attempts" "$(calls)" 3

echo ""
echo "--- Results ---"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo ""
if [ "$FAIL" -gt 0 ]; then
  echo "FAIL: $FAIL test(s) failed"
  exit 1
fi
echo "PASS: all $PASS tests passed"
