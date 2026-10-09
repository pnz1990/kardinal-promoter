// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	// maxPackObjectBytes bounds the inflated size of one object in a pathGlob
	// fetch (a commit or a tree; blobs are filtered out when the server
	// supports partial clone).
	maxPackObjectBytes = 32 << 20
	// maxPackInflatedBytes bounds the inflated size of all objects of one
	// pathGlob fetch together.
	maxPackInflatedBytes = 128 << 20
)

// errPackTooLarge is returned when a pack's objects inflate past the limits.
var errPackTooLarge = errors.New("the fetched objects are too large")

// readPack reads a pack of at most maxPackBytes from r, checks the inflated
// size of every object (checkPack) and loads it into a new in-memory store
// whose objects may total at most maxPackInflatedBytes. A small pack can
// inflate to gigabytes (a zlib bomb, or a delta whose target is huge), and the
// store keeps every object inflated, so the wire size alone does not bound
// memory.
func readPack(r io.Reader) (*memory.Storage, error) {
	raw, err := io.ReadAll(&limitedPack{r: r, left: maxPackBytes})
	if err != nil {
		return nil, err
	}
	if err := checkPack(raw, maxPackObjectBytes, maxPackInflatedBytes); err != nil {
		return nil, err
	}
	st := &budgetStorage{Storage: memory.NewStorage(), left: maxPackInflatedBytes}
	if err := packfile.UpdateObjectStorage(st, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("read pack: %w", err)
	}
	return st.Storage, nil
}

// checkPack scans pack without keeping any object. Every object's declared
// inflated size must be at most perObject, and so must the target size a
// delta declares; together they must be at most total. The scanner inflates
// each object into a writer bounded by its declared size, so an object that
// inflates past its header is an error too. Inflated content is discarded.
func checkPack(pack []byte, perObject, total int64) error {
	s := packfile.NewScanner(bytes.NewReader(pack))
	_, count, err := s.Header()
	if err != nil {
		return fmt.Errorf("read pack header: %w", err)
	}
	var sum int64
	for i := uint32(0); i < count; i++ {
		oh, err := s.NextObjectHeader()
		if err != nil {
			return fmt.Errorf("read pack object header: %w", err)
		}
		if oh.Length > perObject {
			return fmt.Errorf("%w: an object inflates to %d bytes (limit %d)", errPackTooLarge, oh.Length, perObject)
		}
		size := oh.Length
		isDelta := oh.Type == plumbing.OFSDeltaObject || oh.Type == plumbing.REFDeltaObject
		var delta bytes.Buffer
		w := io.Discard
		if isDelta {
			w = &delta
		}
		if _, _, err := s.NextObject(w); err != nil {
			return fmt.Errorf("read pack object: %w", err)
		}
		if isDelta {
			target, err := deltaTargetSize(delta.Bytes())
			if err != nil {
				return err
			}
			if target > perObject {
				return fmt.Errorf("%w: a delta declares a %d byte object (limit %d)", errPackTooLarge, target, perObject)
			}
			size = target
		}
		if sum += size; sum > total {
			return fmt.Errorf("%w: the objects inflate to more than %d bytes", errPackTooLarge, total)
		}
	}
	return nil
}

// deltaTargetSize reads the target size from a git delta: two
// little-endian base-128 varints, the source size and the target size.
func deltaTargetSize(d []byte) (int64, error) {
	pos := 0
	read := func() (int64, error) {
		var v int64
		for shift := uint(0); ; shift += 7 {
			if pos >= len(d) || shift > 56 {
				return 0, fmt.Errorf("read pack object: invalid delta header")
			}
			b := d[pos]
			pos++
			v |= int64(b&0x7f) << shift
			if b&0x80 == 0 {
				return v, nil
			}
		}
	}
	if _, err := read(); err != nil {
		return 0, err
	}
	return read()
}

// budgetStorage is an in-memory store that fails once the objects stored in
// it total more than left bytes. checkPack already bounds the sizes; this is
// the second line, on what is actually stored.
type budgetStorage struct {
	*memory.Storage
	left int64
}

// SetEncodedObject stores obj unless it exceeds the remaining budget.
func (b *budgetStorage) SetEncodedObject(obj plumbing.EncodedObject) (plumbing.Hash, error) {
	if b.left -= obj.Size(); b.left < 0 {
		return plumbing.ZeroHash, fmt.Errorf("%w: the objects inflate to more than %d bytes", errPackTooLarge, maxPackInflatedBytes)
	}
	return b.Storage.SetEncodedObject(obj)
}
