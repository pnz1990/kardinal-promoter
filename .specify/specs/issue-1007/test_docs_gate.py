#!/usr/bin/env python3
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
"""
Unit tests for QA §3b.5 docs gate logic.
Tests: user-visible feature detection, Layer 1 auto-doc detection,
docs/ change detection, and WRONG finding trigger conditions.
"""
import re
import sys


# ── Helpers extracted from qa.md §3b.5 (same logic, testable) ─────────────

USER_VISIBLE_KEYWORDS = [
    'cli', 'command', 'flag', 'crd', 'field', 'spec.', 'status.',
    'ui', 'dashboard', 'button', 'panel', 'page', 'view',
    'api', 'endpoint', 'webhook', 'event',
    'kardinal ', 'kubectl', 'output', 'display',
]
LAYER1_AUTO_KEYWORDS = [
    'auto-generated', 'auto-documented', 'automatically', 'generated from',
    'crds auto', 'completion auto', 'openapi', 'swagger',
]


def find_moved_to_present(diff: str) -> list[str]:
    """Find lines added to ✅ Present section in diff."""
    return re.findall(r'^\+- ✅ (.+)', diff, re.MULTILINE)


def is_user_visible(item: str) -> bool:
    """Check if a ✅ Present item is user-visible."""
    item_lower = item.lower()
    is_layer1 = any(kw in item_lower for kw in LAYER1_AUTO_KEYWORDS)
    is_visible = any(kw in item_lower for kw in USER_VISIBLE_KEYWORDS)
    return is_visible and not is_layer1


def docs_changed_in_diff(diff: str) -> bool:
    """Check if diff includes changes to customer-facing docs/ files.
    Excludes docs/design/ and docs/aide/ (internal docs).
    """
    return bool(re.search(
        r'^(---|\+\+\+)\s+[ab]/docs/(?!design/|aide/)[\w/-]+\.md',
        diff, re.MULTILINE))


# ── Tests ────────────────────────────────────────────────────────────────


def test_find_moved_to_present_basic():
    """Detects ✅ Present items added in a diff."""
    diff = """\
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,6 +10,8 @@
 ## Present (✅)
 
+- ✅ **Fleet dashboard** with pipeline status (PR #99, 2026-01-01)
+- ✅ **CLI kardinal get** command implemented (PR #98, 2026-01-01)
 
 ## Future (🔲)
"""
    items = find_moved_to_present(diff)
    assert len(items) == 2, f"Expected 2 items, got {items}"
    assert any('Fleet dashboard' in i for i in items)
    assert any('CLI kardinal get' in i for i in items)
    print("✅ test_find_moved_to_present_basic passed")


def test_find_moved_to_present_none():
    """Returns empty list when no ✅ items added."""
    diff = """\
--- a/docs/design/foo.md
+++ b/docs/design/foo.md
@@ -1 +1 @@
-old line
+new line
"""
    items = find_moved_to_present(diff)
    assert items == [], f"Expected empty, got {items}"
    print("✅ test_find_moved_to_present_none passed")


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
    assert is_user_visible("Fleet dashboard shows pipeline topology")
    print("✅ test_is_user_visible_ui passed")


def test_is_not_user_visible_internal():
    """Internal implementation detail is not user-visible."""
    assert not is_user_visible("Graph translator refactoring for node ID normalization")
    print("✅ test_is_not_user_visible_internal passed")


def test_is_layer1_auto_doc_skipped():
    """Layer 1 auto-generated features are skipped even with user-visible keywords."""
    # Contains 'auto-generated' — should NOT be flagged
    item = "CRD auto-generated kubectl completion command via controller-gen"
    assert not is_user_visible(item), f"Expected not user-visible (layer1): {item}"
    print("✅ test_is_layer1_auto_doc_skipped passed")


def test_docs_changed_in_diff_true():
    """Diff with docs/ changes returns True."""
    diff = """\
--- a/docs/cli-reference.md
+++ b/docs/cli-reference.md
@@ -10,0 +10 @@
+### kardinal override
"""
    assert docs_changed_in_diff(diff)
    print("✅ test_docs_changed_in_diff_true passed")


def test_docs_changed_in_diff_false():
    """Diff with no docs/ changes returns False."""
    diff = """\
--- a/pkg/foo/bar.go
+++ b/pkg/foo/bar.go
@@ -1 +1 @@
-old
+new
"""
    assert not docs_changed_in_diff(diff)
    print("✅ test_docs_changed_in_diff_false passed")


def test_wrong_finding_triggered():
    """WRONG finding should trigger when: ✅ Present with user-visible feature AND no docs/ changes."""
    diff = """\
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,0 +11 @@
+- ✅ **Fleet dashboard with kardinal status UI** (PR #99)
--- a/pkg/ui/handler.go
+++ b/pkg/ui/handler.go
@@ -1 +1 @@
-old
+new
"""
    items = find_moved_to_present(diff)
    user_vis = [i for i in items if is_user_visible(i)]
    docs_ok = docs_changed_in_diff(diff)

    assert len(user_vis) > 0, f"Expected user-visible items"
    assert not docs_ok, "Expected no docs changes"
    # → WRONG finding should fire
    print("✅ test_wrong_finding_triggered passed")


def test_wrong_finding_not_triggered_when_docs_updated():
    """WRONG finding should NOT trigger when docs/ files are changed."""
    diff = """\
--- a/docs/design/06-kardinal-ui.md
+++ b/docs/design/06-kardinal-ui.md
@@ -10,0 +11 @@
+- ✅ **Fleet dashboard** (PR #99)
--- a/docs/ui-reference.md
+++ b/docs/ui-reference.md
@@ -1 +1 @@
+## Fleet Dashboard
"""
    items = find_moved_to_present(diff)
    user_vis = [i for i in items if is_user_visible(i)]
    docs_ok = docs_changed_in_diff(diff)

    assert len(user_vis) > 0
    assert docs_ok  # docs updated — WRONG finding should NOT fire
    print("✅ test_wrong_finding_not_triggered_when_docs_updated passed")


def main():
    tests = [
        test_find_moved_to_present_basic,
        test_find_moved_to_present_none,
        test_is_user_visible_cli,
        test_is_user_visible_crd,
        test_is_user_visible_ui,
        test_is_not_user_visible_internal,
        test_is_layer1_auto_doc_skipped,
        test_docs_changed_in_diff_true,
        test_docs_changed_in_diff_false,
        test_wrong_finding_triggered,
        test_wrong_finding_not_triggered_when_docs_updated,
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
