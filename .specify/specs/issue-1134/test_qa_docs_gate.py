#!/usr/bin/env python3
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
"""
Unit tests for scripts/qa-docs-gate.sh logic.
Tests the Python logic embedded in the script:
  - Future→Present transition detection
  - User-visible feature classification
  - docs/ change detection
  - WRONG finding trigger conditions
"""
import re
import sys
import subprocess
import os


# ── Logic extracted from qa-docs-gate.sh for testability ─────────────────────

USER_VISIBLE_KEYWORDS = [
    r'\bcli\b', r'\bcrd\b', r'\bui\b', r'\bapi\b',
    r'\bcommand\b', r'\bflag\b', r'\bendpoint\b',
    r'spec\.', r'status\.', r'\bdashboard\b', r'\bweb\b',
    r'\bkubectl\b', r'\bkardinal\b',
]
USER_VISIBLE_RE = re.compile('|'.join(USER_VISIBLE_KEYWORDS), re.IGNORECASE)
LAYER1_KEYWORDS = re.compile(r'layer 1|auto-documented|layer-1', re.IGNORECASE)

REMOVED_FUTURE = re.compile(r'^-\s*-\s*🔲\s*(.+)', re.UNICODE)
ADDED_PRESENT  = re.compile(r'^\+\s*-\s*✅\s*(.+)', re.UNICODE)
DESIGN_DOC_PATTERN = re.compile(r'^diff --git a/(docs/design/[^\s]+\.md)')
DOCS_FILE_PATTERN = re.compile(r'^diff --git a/(docs/(?!design/)[^\s]+)')


def norm(s):
    s = re.sub(r'\s*\(PR #\d+.*?\)', '', s)
    s = re.sub(r'\s*—.*$', '', s)
    s = re.sub(r'\*+', '', s)
    return s.strip().lower()


def find_transitions(diff_text):
    """Find Future→Present transitions in a diff."""
    promoted = []
    current_file = None
    removed_futures = {}
    added_presents = {}

    for line in diff_text.splitlines():
        file_m = DESIGN_DOC_PATTERN.match(line)
        if file_m:
            if current_file:
                for key, desc in removed_futures.items():
                    if key in added_presents:
                        promoted.append(desc)
            current_file = file_m.group(1)
            removed_futures = {}
            added_presents = {}
            continue

        if current_file is None:
            continue

        rm = REMOVED_FUTURE.match(line)
        if rm:
            desc = rm.group(1).strip()
            removed_futures[norm(desc)] = desc
            continue

        am = ADDED_PRESENT.match(line)
        if am:
            desc = am.group(1).strip()
            added_presents[norm(desc)] = desc
            continue

    if current_file:
        for key, desc in removed_futures.items():
            if key in added_presents:
                promoted.append(desc)

    return promoted


def is_user_visible(desc):
    return bool(USER_VISIBLE_RE.search(desc))


def is_layer1(desc):
    return bool(LAYER1_KEYWORDS.search(desc))


def docs_changed(diff_text):
    for line in diff_text.splitlines():
        if DOCS_FILE_PATTERN.match(line):
            return True
    return False


# ── Tests ─────────────────────────────────────────────────────────────────────

def test_find_transitions_basic():
    """Detects a single Future→Present move."""
    diff = """\
diff --git a/docs/design/06-kardinal-ui.md b/docs/design/06-kardinal-ui.md
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,0 +11 @@
-- 🔲 Fleet dashboard UI showing pipeline status
+- ✅ Fleet dashboard UI showing pipeline status (PR #99, 2026-04-22)
"""
    result = find_transitions(diff)
    assert len(result) == 1, f"Expected 1 transition, got {len(result)}: {result}"
    assert 'Fleet dashboard' in result[0]
    print("✅ test_find_transitions_basic passed")


def test_find_transitions_none():
    """No transitions when no design doc lines match."""
    diff = """\
diff --git a/pkg/foo/bar.go b/pkg/foo/bar.go
--- a/pkg/foo/bar.go
+++ b/pkg/foo/bar.go
@@ -1 +1 @@
-old
+new
"""
    result = find_transitions(diff)
    assert len(result) == 0, f"Expected 0 transitions, got {len(result)}"
    print("✅ test_find_transitions_none passed")


def test_find_transitions_no_matching_present():
    """Removed Future item with no matching Present item = no transition."""
    diff = """\
diff --git a/docs/design/06-kardinal-ui.md b/docs/design/06-kardinal-ui.md
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,0 +11 @@
-- 🔲 Fleet dashboard UI
+- ✅ Completely different feature (PR #99)
"""
    result = find_transitions(diff)
    # Description keys don't match after normalization — no transition
    assert len(result) == 0, f"Expected 0 transitions (no matching key), got {len(result)}"
    print("✅ test_find_transitions_no_matching_present passed")


def test_is_user_visible_cli():
    """CLI feature is user-visible."""
    assert is_user_visible("kardinal override command with --reason flag")
    print("✅ test_is_user_visible_cli passed")


def test_is_user_visible_crd():
    """CRD field is user-visible."""
    assert is_user_visible("New spec.bakeMinutes CRD field for soak time")
    print("✅ test_is_user_visible_crd passed")


def test_is_user_visible_ui():
    """UI dashboard is user-visible."""
    assert is_user_visible("Fleet dashboard shows pipeline topology in the web UI")
    print("✅ test_is_user_visible_ui passed")


def test_is_not_user_visible_internal():
    """Internal refactoring is not user-visible."""
    assert not is_user_visible("Graph translator refactoring for node ID normalization")
    print("✅ test_is_not_user_visible_internal passed")


def test_is_layer1_exemption():
    """Layer 1 auto-documented feature passes without docs check."""
    item = "CRD fields auto-documented by controller-gen — Layer 1 auto-documented"
    assert is_layer1(item)
    print("✅ test_is_layer1_exemption passed")


def test_docs_changed_true():
    """Diff with docs/ changes (not docs/design/) returns True."""
    diff = """\
diff --git a/docs/cli-reference.md b/docs/cli-reference.md
--- a/docs/cli-reference.md
+++ b/docs/cli-reference.md
@@ -10,0 +10 @@
+### kardinal override
"""
    assert docs_changed(diff), "Expected docs_changed to return True"
    print("✅ test_docs_changed_true passed")


def test_docs_changed_design_not_counted():
    """docs/design/ changes alone do NOT satisfy the docs gate."""
    diff = """\
diff --git a/docs/design/41-published-docs-freshness.md b/docs/design/41-published-docs-freshness.md
--- a/docs/design/41-published-docs-freshness.md
+++ b/docs/design/41-published-docs-freshness.md
@@ -50,6 +50,8 @@
-- 🔲 QA docs gate
+- ✅ QA docs gate (PR #1134)
"""
    assert not docs_changed(diff), "docs/design/ should NOT count as docs/ change"
    print("✅ test_docs_changed_design_not_counted passed")


def test_wrong_scenario():
    """WRONG: user-visible feature promoted with no docs/ update."""
    diff = """\
diff --git a/docs/design/06-kardinal-ui.md b/docs/design/06-kardinal-ui.md
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,0 +11 @@
-- 🔲 Fleet dashboard UI showing pipeline status
+- ✅ Fleet dashboard UI showing pipeline status (PR #99, 2026-04-22)
diff --git a/web/src/components/Dashboard.tsx b/web/src/components/Dashboard.tsx
--- a/web/src/components/Dashboard.tsx
+++ b/web/src/components/Dashboard.tsx
@@ -1 +1 @@
-old
+new
"""
    transitions = find_transitions(diff)
    assert len(transitions) == 1
    assert is_user_visible(transitions[0])
    assert not is_layer1(transitions[0])
    assert not docs_changed(diff)  # no docs/*.md changes
    # → WRONG should fire
    print("✅ test_wrong_scenario passed")


def test_pass_scenario():
    """PASS: user-visible feature promoted WITH docs/ update."""
    diff = """\
diff --git a/docs/design/06-kardinal-ui.md b/docs/design/06-kardinal-ui.md
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,0 +11 @@
-- 🔲 Fleet dashboard UI showing pipeline status
+- ✅ Fleet dashboard UI showing pipeline status (PR #99, 2026-04-22)
diff --git a/docs/ui-reference.md b/docs/ui-reference.md
--- a/docs/ui-reference.md
+++ b/docs/ui-reference.md
@@ -1 +1 @@
+## Fleet Dashboard
"""
    transitions = find_transitions(diff)
    assert len(transitions) == 1
    assert is_user_visible(transitions[0])
    assert docs_changed(diff)  # docs changed → PASS
    print("✅ test_pass_scenario passed")


def test_script_skip_no_pr_num():
    """Script exits 0 with skip message when no PR_NUM provided."""
    script = os.path.join(os.path.dirname(__file__), '..', '..', '..', 'scripts', 'qa-docs-gate.sh')
    script = os.path.abspath(script)
    if not os.path.exists(script):
        print("✅ test_script_skip_no_pr_num SKIPPED (script not found at expected path)")
        return
    result = subprocess.run(
        ['bash', script],
        capture_output=True, text=True,
        env={**os.environ, 'PR_NUM': '', 'REPO': 'pnz1990/kardinal-promoter'}
    )
    assert result.returncode == 0, f"Expected exit 0, got {result.returncode}: {result.stdout}"
    assert 'SKIPPED' in result.stdout, f"Expected SKIPPED in output: {result.stdout}"
    print("✅ test_script_skip_no_pr_num passed")


def main():
    tests = [
        test_find_transitions_basic,
        test_find_transitions_none,
        test_find_transitions_no_matching_present,
        test_is_user_visible_cli,
        test_is_user_visible_crd,
        test_is_user_visible_ui,
        test_is_not_user_visible_internal,
        test_is_layer1_exemption,
        test_docs_changed_true,
        test_docs_changed_design_not_counted,
        test_wrong_scenario,
        test_pass_scenario,
        test_script_skip_no_pr_num,
    ]
    failed = 0
    for test in tests:
        try:
            test()
        except AssertionError as e:
            print(f"❌ {test.__name__} FAILED: {e}")
            failed += 1
        except Exception as e:
            print(f"❌ {test.__name__} ERROR: {e}")
            failed += 1

    if failed:
        print(f"\n{failed}/{len(tests)} tests FAILED.")
        sys.exit(1)
    else:
        print(f"\n{len(tests)}/{len(tests)} tests PASSED.")
        sys.exit(0)


if __name__ == '__main__':
    main()
