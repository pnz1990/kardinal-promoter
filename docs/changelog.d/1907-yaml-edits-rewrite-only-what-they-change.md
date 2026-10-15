---
section: Fixed
---

- **YAML edits rewrite only what they change** (#1907) — kustomize-set-image, helm-set-image and the yaml strategy wrote the whole file back through the YAML encoder, which does not reproduce every scalar: a folded block scalar with more-indented lines (`key: >`) came back as `>-` with other line breaks, so a value the step did not own changed. They now write the file's own bytes with only the edited mapping entries replaced; comments, quoting, indentation, block scalars and flow collections elsewhere stay byte for byte. A file the splice cannot handle is encoded whole as before, and the edit is refused when that would change any other value.
