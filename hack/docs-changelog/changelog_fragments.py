# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
"""MkDocs hook: the changelog page lists the unreleased entries.

Each change adds its own file, docs/changelog.d/<issue>-<slug>.md (front
matter "section: <Section>", then its entries), so two pull requests never
edit the same lines of docs/changelog.md. This hook puts them under
"## [Unreleased]", at the marker line, in the order and with the headings
hack/changelog renders (go run ./hack/changelog unreleased; test/hack checks
the two agree). A release writes them into docs/changelog.md for good
(go run ./hack/changelog release vX.Y.Z).
"""

import os
import re

MARKER = ("<!-- changelog.d: the unreleased entries are the files in docs/changelog.d "
          "(CONTRIBUTING.md, Changelog) -->")
SECTIONS = ["Upgrade", "Added", "Changed", "Deprecated", "Removed", "Fixed", "Security",
            "Documentation", "Development"]
HEADINGS = {"Upgrade": "Before you upgrade"}
FRONT = re.compile(r"\A---\n(.*?)\n---\n(.*)\Z", re.S)
NAME = re.compile(r"^[0-9]+(-[a-z0-9][a-z0-9-]*)?\.md$")


def read_fragments(directory):
    """The fragments of directory as (name, section, body), in order."""
    out = []
    if not os.path.isdir(directory):
        return out
    for name in os.listdir(directory):
        if name == "README.md" or name.startswith("."):
            continue
        if not NAME.match(name):
            raise ValueError("%s: name it <issue or PR number>-<slug>.md" % name)
        with open(os.path.join(directory, name), encoding="utf-8") as f:
            text = f.read().replace("\r\n", "\n")
        m = FRONT.match(text)
        if not m:
            raise ValueError("%s: no front matter" % name)
        section = None
        for line in m.group(1).split("\n"):
            key, _, value = line.partition(":")
            if key.strip() != "section":
                raise ValueError("%s: front matter line %r" % (name, line))
            section = value.strip()
        if section not in SECTIONS:
            raise ValueError("%s: section %r" % (name, section))
        out.append((name, section, m.group(2).strip()))
    out.sort(key=lambda f: (int(f[0].split("-", 1)[0].removesuffix(".md")), f[0]))
    return out


def render(fragments):
    """The fragments as changelog sections, as hack/changelog's Render."""
    parts = []
    for section in SECTIONS:
        bodies = [body for _, s, body in fragments if s == section]
        if bodies:
            parts.append("### %s\n\n%s\n" % (HEADINGS.get(section, section), "\n".join(bodies)))
    return "\n".join(parts)


def on_page_markdown(markdown, page, config, files):
    if page.file.src_uri != "changelog.md" or MARKER not in markdown:
        return markdown
    fragments = read_fragments(os.path.join(config["docs_dir"], "changelog.d"))
    return markdown.replace(MARKER, render(fragments) if fragments else "Nothing yet.\n", 1)
