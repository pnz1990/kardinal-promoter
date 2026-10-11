// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package style

import (
	"io"
	"strings"
	"unicode/utf8"
)

// folds are the non-ASCII characters the cluster's messages use, in ASCII.
var folds = map[rune]string{'—': "-", '–': "-", '→': "->", '←': "<-", '…': "...", '“': `"`, '”': `"`, '‘': "'", '’': "'", '•': "*"}

// Fold is text in ASCII, for a stream that gets plain words: a dash is "-",
// an arrow "->", and any other non-ASCII character "?". The CLI's own text is
// ASCII already there; this covers what it prints from the cluster (a step's
// message, a Pipeline's summary).
func Fold(text string) string {
	if isASCII(text) {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		switch {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case folds[r] != "":
			b.WriteString(folds[r])
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// Folder writes to its writer through Fold. A character split across two
// writes is folded whole.
type Folder struct {
	w       io.Writer
	partial []byte
}

// NewFolder is a Folder writing to w.
func NewFolder(w io.Writer) *Folder { return &Folder{w: w} }

// Write folds p and writes it, keeping an incomplete trailing character for
// the next write. It reports len(p) written on success.
func (f *Folder) Write(p []byte) (int, error) {
	buf := make([]byte, 0, len(f.partial)+len(p))
	buf = append(append(buf, f.partial...), p...)
	cut := len(buf)
	for i := len(buf) - 1; i >= 0 && i >= len(buf)-utf8.UTFMax; i-- {
		if utf8.RuneStart(buf[i]) {
			if !utf8.FullRune(buf[i:]) {
				cut = i
			}
			break
		}
	}
	f.partial = append([]byte(nil), buf[cut:]...)
	if _, err := io.WriteString(f.w, Fold(string(buf[:cut]))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Underlying is the writer f writes to.
func (f *Folder) Underlying() io.Writer { return f.w }
