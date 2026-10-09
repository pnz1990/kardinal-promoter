// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" //nolint:gosec // the pack trailer is SHA-1
	"encoding/binary"
	"io"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// packObject is one object of a hand-built pack.
type packObject struct {
	typ        byte  // 3 blob, 6 ofs-delta
	declared   int64 // the size in the object header
	content    io.Reader
	baseOffset int64 // ofs-delta: distance back to the base object
}

// buildPack writes a version 2 pack of objs.
func buildPack(t *testing.T, objs ...packObject) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("PACK")
	_ = binary.Write(&b, binary.BigEndian, uint32(2))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(objs)))
	for _, o := range objs {
		size := o.declared
		c := o.typ<<4 | byte(size&0x0f)
		size >>= 4
		for size > 0 {
			b.WriteByte(c | 0x80)
			c = byte(size & 0x7f)
			size >>= 7
		}
		b.WriteByte(c)
		if o.typ == 6 {
			off := o.baseOffset
			buf := []byte{byte(off & 0x7f)}
			for off >>= 7; off > 0; off >>= 7 {
				off--
				buf = append([]byte{0x80 | byte(off&0x7f)}, buf...)
			}
			b.Write(buf)
		}
		zw, err := zlib.NewWriterLevel(&b, zlib.BestSpeed)
		require.NoError(t, err)
		_, err = io.CopyBuffer(zw, o.content, make([]byte, 4<<20))
		require.NoError(t, err)
		require.NoError(t, zw.Close())
	}
	sum := sha1.Sum(b.Bytes()) //nolint:gosec
	b.Write(sum[:])
	return b.Bytes()
}

// zeros is an io.Reader of n zero bytes that allocates nothing.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > z.n {
		p = p[:z.n]
	}
	clear(p)
	z.n -= int64(len(p))
	return len(p), nil
}

func heapNow() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestReadPack_RejectsInflationBombs: a pack whose wire size is small but
// whose objects inflate to far more than the limits is refused before any
// object is kept, so a pathGlob fetch from a hostile git server cannot take
// the controller's memory: a 512 MiB blob that compresses to about 500 KB
// (zlib bomb), an object that inflates past its own header, a delta that
// declares a 1 GiB target, and many objects that together pass the budget.
func TestReadPack_RejectsInflationBombs(t *testing.T) {
	const bomb = 512 << 20
	zlibBomb := buildPack(t, packObject{typ: 3, declared: bomb, content: &zeros{n: bomb}})
	require.Less(t, len(zlibBomb), 1<<20, "the bomb is small on the wire")

	before := heapNow()
	_, err := readPack(bytes.NewReader(zlibBomb))
	require.ErrorIs(t, err, errPackTooLarge)
	assert.Contains(t, err.Error(), "an object inflates to 536870912 bytes")
	assert.Less(t, int64(heapNow())-int64(before), int64(64<<20), "the bomb is not inflated into memory")

	lying := buildPack(t, packObject{typ: 3, declared: 10, content: &zeros{n: 1 << 20}})
	_, err = readPack(bytes.NewReader(lying))
	require.Error(t, err, "an object that inflates past its header")

	// A delta: source size 3, target size 1 GiB (varints), then garbage.
	deltaHdr := []byte{3, 0x80, 0x80, 0x80, 0x80, 0x04}
	deltaBomb := buildPack(t,
		packObject{typ: 3, declared: 3, content: bytes.NewReader([]byte("abc"))},
		packObject{typ: 6, declared: int64(len(deltaHdr)), content: bytes.NewReader(deltaHdr), baseOffset: 12})
	err = checkPack(deltaBomb, maxPackObjectBytes, maxPackInflatedBytes)
	require.ErrorIs(t, err, errPackTooLarge)
	assert.Contains(t, err.Error(), "a delta declares a 1073741824 byte object")

	var many []packObject
	for range 3 {
		many = append(many, packObject{typ: 3, declared: 60, content: &zeros{n: 60}})
	}
	err = checkPack(buildPack(t, many...), 100, 150)
	require.ErrorIs(t, err, errPackTooLarge)
	assert.Contains(t, err.Error(), "the objects inflate to more than 150 bytes")
	require.NoError(t, checkPack(buildPack(t, many...), 100, 180), "within the budget")
}

// TestBudgetStorage: a valid pack loads; the store refuses objects past its
// budget.
func TestBudgetStorage(t *testing.T) {
	st, err := readPack(bytes.NewReader(buildPack(t, packObject{typ: 3, declared: 5, content: bytes.NewReader([]byte("hello"))})))
	require.NoError(t, err)
	it, err := st.IterEncodedObjects(plumbing.AnyObject)
	require.NoError(t, err)
	n := 0
	require.NoError(t, it.ForEach(func(plumbing.EncodedObject) error { n++; return nil }))
	assert.Equal(t, 1, n)

	b := &budgetStorage{Storage: memory.NewStorage(), left: 3}
	obj := &plumbing.MemoryObject{}
	obj.SetType(plumbing.BlobObject)
	_, _ = obj.Write([]byte("hello"))
	_, err = b.SetEncodedObject(obj)
	require.ErrorIs(t, err, errPackTooLarge)
}
