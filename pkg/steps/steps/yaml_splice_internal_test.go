// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEncode_WritesOnlyTheEdit: an edit rewrites only the mapping entries
// it changed (#1907). A folded block scalar the edit does not own keeps its
// chomping, indentation and value (the encoder used to write "0: >\n 0\n  0"
// back as ">-" with other line breaks), and so do comments, quoting, a
// four-space indentation and flow collections; a new key goes after the
// last entry, before the trailing comments. An edit the encoder cannot
// write without changing other values is refused.
// Covers STEP-YAML-MINIMAL-01.
func TestEncode_WritesOnlyTheEdit(t *testing.T) {
	for _, tc := range []struct {
		name, in, path, value, want string
	}{
		{name: "folded scalar beside a new key (#1907)", in: "0: >\n 0\n  0\n", path: "images", value: "x",
			want: "0: >\n 0\n  0\nimages: x\n"},
		{name: "a nested scalar in a four-space file", in: "# values\nimage:\n    repository: 'ghcr.io/org/app'  # where\n    tag: \"1.0.0\" # pinned\nlist: {a: , b: [1, 2]}\nnote: |\n    keep\n      me\n# end\n",
			path: "image.tag", value: "1.1.0",
			want: "# values\nimage:\n    repository: 'ghcr.io/org/app'  # where\n    tag: \"1.1.0\" # pinned\nlist: {a: , b: [1, 2]}\nnote: |\n    keep\n      me\n# end\n"},
		{name: "a new key before the trailing comment", in: "a: 1\n\n# end\n", path: "b", value: "2",
			want: "a: 1\nb: \"2\"\n\n# end\n"},
		{name: "an item of a block sequence", in: "spec:\n  containers:\n    - name: app\n      image: old # set by CI\n    - name: side\n      image: >-\n        keep\n",
			path: "spec.containers[name=app].image", value: "new",
			want: "spec:\n  containers:\n    - name: app\n      image: new # set by CI\n    - name: side\n      image: >-\n        keep\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := parseYAMLMapping([]byte(tc.in))
			require.NoError(t, err)
			if tc.path == "images" {
				setMapScalar(d.root(), "images", tc.value)
			} else {
				require.NoError(t, setYAMLPath(d.root(), tc.path, tc.value))
			}
			out, err := d.encode()
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(out))
		})
	}

	// A file the splice does not handle (a key written twice) is encoded
	// whole; when the encoder would change the folded scalar, the edit is
	// refused instead of written.
	d, err := parseYAMLMapping([]byte("a: >\n 0\n  0\na: 1\n"))
	require.NoError(t, err)
	setMapScalar(d.root(), "b", "x")
	_, err = d.encode()
	assert.ErrorIs(t, err, errReencodeChanges)
}
