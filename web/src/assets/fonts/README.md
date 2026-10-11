# Fonts

The UI and the docs self-host three variable fonts, each under the SIL Open Font
License 1.1. Its full text ships next to each font (`*-OFL.txt`). None of the
three licenses declares a Reserved Font Name, so the subset files keep the
original family names.

| File | Family | Upstream (repository, commit, file) | License |
|---|---|---|---|
| `Archivo-Variable.woff2` | Archivo (`wght` 100–900, `wdth` 62–125) | github.com/Omnibus-Type/Archivo @ `211127690e8ff106c36c935f7e5e697114cff103`, `fonts/variable/Archivo[wdth,wght].ttf` | `Archivo-OFL.txt` |
| `AtkinsonHyperlegibleNext-Variable.woff2` | Atkinson Hyperlegible Next (`wght` 200–800) | github.com/googlefonts/atkinson-hyperlegible-next @ `7925f50f649b3813257faf2f4c0b381011f434f1`, `fonts/variable/AtkinsonHyperlegibleNext[wght].ttf` | `AtkinsonHyperlegibleNext-OFL.txt` |
| `AtkinsonHyperlegibleMono-Variable.woff2` | Atkinson Hyperlegible Mono (`wght` 200–800) | github.com/googlefonts/atkinson-hyperlegible-next-mono @ `154d50362016cc3e873eb21d242cd0772384c8f9`, `fonts/variable/AtkinsonHyperlegibleMono[wght].ttf` | `AtkinsonHyperlegibleMono-OFL.txt` |

The woff2 files are subsets made with `hack/subset-fonts.sh`. Archivo keeps
Latin. The two Atkinson fonts keep Latin and Latin Extended. Every axis is
kept. `scripts/gen-tokens.mjs` declares the `@font-face` rules and copies this
directory to `docs/assets/fonts/`.
