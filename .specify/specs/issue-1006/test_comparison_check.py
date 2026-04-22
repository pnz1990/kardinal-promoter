#!/usr/bin/env python3
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
"""
Unit tests for PM §5n comparison doc accuracy check parsing logic.
Tests the ❌ row extraction and ✅ Present item matching.
"""
import re
import sys
import os

# ── Helpers extracted from pm.md §5n (same logic, testable) ───────────────


def extract_missing_features(comparison_content: str) -> list[str]:
    """Extract feature names from ❌ rows in comparison.md table."""
    missing = []
    for line in comparison_content.splitlines():
        if not line.startswith('|') or line.startswith('| ---') or line.startswith('|---'):
            continue
        cells = [c.strip() for c in line.split('|')[1:-1]]
        if len(cells) < 2:
            continue
        feature_name = cells[0].strip('*').strip()
        if feature_name.lower() in ('feature', '**feature**'):
            continue
        kardinal_val = cells[1] if len(cells) > 1 else ''
        if '❌' in kardinal_val or kardinal_val.strip().lower() == 'no':
            missing.append(feature_name)
    return missing


def extract_present_terms(design_content: str) -> list[str]:
    """Extract ✅ Present item descriptions from a design doc."""
    terms = []
    present_match = re.search(
        r'^## Present.*?\n(.*?)(?=^## |\Z)', design_content,
        re.MULTILINE | re.DOTALL)
    if present_match:
        items = re.findall(r'^- ✅ (.+)', present_match.group(1), re.MULTILINE)
        for item in items:
            desc = re.sub(r'\s*\(PR.*$', '', item).strip().lower()[:80]
            if desc:
                terms.append(desc)
    return terms


def feature_matches_present(feature: str, present_terms: list[str]) -> bool:
    """Check if a feature matches any ✅ Present item."""
    feature_key = feature.lower().strip('*').strip()[:60]
    match_found = any(feature_key in term or term in feature_key
                      for term in present_terms
                      if len(feature_key) > 4 and len(term) > 4)
    if not match_found:
        feature_words = set(w for w in re.findall(r'\w+', feature_key) if len(w) > 4)
        match_found = any(
            bool(feature_words & set(w for w in re.findall(r'\w+', term) if len(w) > 4))
            for term in present_terms
        )
    return match_found


# ── Tests ─────────────────────────────────────────────────────────────────


def test_extract_missing_features_basic():
    """Basic extraction of ❌ rows from a comparison table."""
    content = """
| Feature | kardinal | Kargo | GitOps Promoter |
|---|---|---|---|
| DAG pipelines | Yes | No | No |
| Policy gates | Yes | ❌ | ❌ |
| Rollback | Yes | No | No |
| Multi-cluster | ❌ | Yes | Yes |
""".strip()
    missing = extract_missing_features(content)
    assert 'Multi-cluster' in missing, f"Expected Multi-cluster in {missing}"
    # 'Policy gates' row: kardinal=Yes, so not ❌ for kardinal
    assert 'Policy gates' not in missing, f"Policy gates should not be in {missing}"
    print("✅ test_extract_missing_features_basic passed")


def test_extract_missing_features_no_suffix():
    """'No' cell value treated as ❌."""
    content = """
| Feature | kardinal | Kargo |
|---|---|---|
| DORA metrics | No | Yes |
| CLI | Yes | Yes |
""".strip()
    missing = extract_missing_features(content)
    assert 'DORA metrics' in missing, f"Expected DORA metrics in {missing}"
    assert 'CLI' not in missing, f"CLI should not be in {missing}"
    print("✅ test_extract_missing_features_no_suffix passed")


def test_extract_missing_features_empty_table():
    """Empty table returns empty list."""
    content = "No table here."
    missing = extract_missing_features(content)
    assert missing == [], f"Expected empty list, got {missing}"
    print("✅ test_extract_missing_features_empty_table passed")


def test_extract_present_terms_basic():
    """Extract ✅ Present items from design doc."""
    content = """
## Present (✅)

- ✅ **Multi-cluster support** via Pipeline CRD (PR #42, 2026-01-01)
- ✅ **CLI with rollback** command implemented (PR #99, 2026-02-01)

## Future (🔲)

- 🔲 Some future item
""".strip()
    terms = extract_present_terms(content)
    assert any('multi-cluster' in t for t in terms), f"Expected multi-cluster in {terms}"
    assert any('cli' in t or 'rollback' in t for t in terms), f"Expected cli/rollback in {terms}"
    print("✅ test_extract_present_terms_basic passed")


def test_feature_matches_present_exact_substring():
    """Feature name matches ✅ Present item via substring."""
    present_terms = [
        'multi-cluster support via pipeline crd',
        'dora metrics bundle.status.metrics',
        'cli with rollback command',
    ]
    assert feature_matches_present('Multi-cluster', present_terms)
    assert feature_matches_present('DORA metrics', present_terms)
    assert feature_matches_present('Rollback', present_terms)
    print("✅ test_feature_matches_present_exact_substring passed")


def test_feature_matches_present_no_match():
    """Feature with no ✅ Present item — no match."""
    present_terms = [
        'policy gate cel evaluation',
        'graph-first architecture',
    ]
    # "Automatic discovery" has no overlap with present_terms
    assert not feature_matches_present('Automatic warehouse discovery', present_terms)
    print("✅ test_feature_matches_present_no_match passed")


def test_feature_matches_present_word_level():
    """Word-level matching works for multi-word features."""
    present_terms = [
        'health check deployment argocd flux rollouts',
    ]
    assert feature_matches_present('Health checks', present_terms)
    print("✅ test_feature_matches_present_word_level passed")


def test_no_false_positive_on_short_words():
    """Short words (<=4 chars) are excluded from word matching."""
    present_terms = [
        'cli tool with run command',
    ]
    # 'cli' is 3 chars — should NOT match 'no CLI' via word matching
    # but 'command' is >4 chars and present
    result = feature_matches_present('No', present_terms)
    assert not result, "Short word 'No' should not match"
    print("✅ test_no_false_positive_on_short_words passed")


def test_separator_rows_skipped():
    """Separator rows (|---|) are not parsed as data rows."""
    content = """
| Feature | kardinal | Kargo |
|---|---|---|
| Real feature | ❌ | Yes |
""".strip()
    missing = extract_missing_features(content)
    assert len(missing) == 1, f"Expected 1 item, got {missing}"
    assert 'Real feature' in missing
    print("✅ test_separator_rows_skipped passed")


def main():
    tests = [
        test_extract_missing_features_basic,
        test_extract_missing_features_no_suffix,
        test_extract_missing_features_empty_table,
        test_extract_present_terms_basic,
        test_feature_matches_present_exact_substring,
        test_feature_matches_present_no_match,
        test_feature_matches_present_word_level,
        test_no_false_positive_on_short_words,
        test_separator_rows_skipped,
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
