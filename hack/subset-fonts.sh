#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# Subsets the UI and docs fonts to woff2. A one-off when a font is vendored or
# updated, not a build step: the output is committed in web/src/assets/fonts
# (scripts/gen-tokens.mjs copies it to docs/assets/fonts).
#
#   hack/subset-fonts.sh DIR   DIR holds the upstream variable TTFs:
#     Archivo[wdth,wght].ttf                  github.com/Omnibus-Type/Archivo, fonts/variable/
#     AtkinsonHyperlegibleNext[wght].ttf      github.com/googlefonts/atkinson-hyperlegible-next, fonts/variable/
#     AtkinsonHyperlegibleMono[wght].ttf      github.com/googlefonts/atkinson-hyperlegible-next-mono, fonts/variable/
#
# Needs pyftsubset (pip install fonttools brotli). Every axis is kept. The body
# and mono faces keep Latin and Latin Extended; Archivo, used for headings and
# labels, keeps Latin, so the three stay within the 180 KB font budget.
set -euo pipefail

src=${1:?usage: $0 DIR}
out="$(git rev-parse --show-toplevel)/web/src/assets/fonts"
common="U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+2000-206F,U+2190-21FF,U+2212,U+2215,U+FEFF,U+FFFD"
extended="$common,U+0100-024F,U+0259,U+0300-0304,U+0308-0309,U+0323,U+0329,U+1E00-1EFF,U+2070-209F,U+20A0-20CF,U+2100-214F,U+2200-22FF,U+2300-23FF,U+2500-257F,U+25A0-25FF,U+2600-26FF,U+2700-27BF,U+2C60-2C7F,U+A720-A7FF"

subset() { # input, output, unicode ranges
  pyftsubset "$src/$1" --unicodes="$3" --layout-features='*' --flavor=woff2 --output-file="$out/$2"
}
subset 'Archivo[wdth,wght].ttf' Archivo-Variable.woff2 "$common"
subset 'AtkinsonHyperlegibleNext[wght].ttf' AtkinsonHyperlegibleNext-Variable.woff2 "$extended"
subset 'AtkinsonHyperlegibleMono[wght].ttf' AtkinsonHyperlegibleMono-Variable.woff2 "$extended"
ls -l "$out"/*.woff2
