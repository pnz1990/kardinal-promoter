---
section: Fixed
---

- **yaml-update keeps a file's line endings and byte order mark** (#2182). A CRLF file was always re-encoded with LF line endings, and a leading UTF-8 byte order mark was dropped, so a promotion PR for a Windows-edited file rewrote every line. The file is now edited in place with its own line ending and BOM kept, as kustomize-set-image and helm-set-image also now do. A file with mixed line endings is written with the one most of its lines use (LF on a tie) ([Pipeline reference](pipeline-reference.md)).
