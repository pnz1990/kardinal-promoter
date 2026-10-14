// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"sort"
)

// Dimension is one parameter of the scenario matrix and its values.
type Dimension struct {
	Name   string
	Values []string
}

// Pair is a value of one dimension with a value of another.
type Pair struct {
	A, B   int // dimension indexes, A < B
	VA, VB string
}

// Pairwise returns rows (one value per dimension) such that every pair of
// values of two dimensions appears in at least one row, except the pairs
// excluded reports. Rows are built greedily (each new row covers as many
// uncovered pairs as it can, AETG-style) and deterministically: the same
// dimensions give the same rows. excluded is also asked about every row:
// a row with an excluded pair is never returned.
func Pairwise(dims []Dimension, excluded func(row []string) bool) [][]string {
	uncovered := map[Pair]bool{}
	for a := 0; a < len(dims); a++ {
		for b := a + 1; b < len(dims); b++ {
			for _, va := range dims[a].Values {
				for _, vb := range dims[b].Values {
					p := Pair{A: a, B: b, VA: va, VB: vb}
					if !pairExcluded(dims, p, excluded) {
						uncovered[p] = true
					}
				}
			}
		}
	}
	var rows [][]string
	for len(uncovered) > 0 {
		row := bestRow(dims, uncovered, excluded)
		if row == nil {
			// The remaining pairs only appear in excluded rows.
			break
		}
		for a := 0; a < len(dims); a++ {
			for b := a + 1; b < len(dims); b++ {
				delete(uncovered, Pair{A: a, B: b, VA: row[a], VB: row[b]})
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// pairExcluded reports whether p can appear in no allowed row: every row
// with p is excluded. It checks the row made of p and the first values,
// then each other value of each other dimension, which is exact for the
// pairwise exclusion rules of the scenario matrix (a rule names at most two
// dimensions).
func pairExcluded(dims []Dimension, p Pair, excluded func(row []string) bool) bool {
	if excluded == nil {
		return false
	}
	row := make([]string, len(dims))
	for i := range dims {
		row[i] = dims[i].Values[0]
	}
	row[p.A], row[p.B] = p.VA, p.VB
	if !excluded(row) {
		return false
	}
	for d := range dims {
		if d == p.A || d == p.B {
			continue
		}
		for _, v := range dims[d].Values {
			r := append([]string(nil), row...)
			r[d] = v
			if !excluded(r) {
				return false
			}
		}
	}
	return true
}

// bestRow starts from the uncovered pair first in order and fills the other
// dimensions one at a time with the value that covers most uncovered pairs
// with the values already chosen.
func bestRow(dims []Dimension, uncovered map[Pair]bool, excluded func(row []string) bool) []string {
	pairs := make([]Pair, 0, len(uncovered))
	for p := range uncovered {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].A != pairs[j].A {
			return pairs[i].A < pairs[j].A
		}
		if pairs[i].B != pairs[j].B {
			return pairs[i].B < pairs[j].B
		}
		if pairs[i].VA != pairs[j].VA {
			return pairs[i].VA < pairs[j].VA
		}
		return pairs[i].VB < pairs[j].VB
	})
	for _, seed := range pairs {
		row := make([]string, len(dims))
		set := make([]bool, len(dims))
		row[seed.A], row[seed.B] = seed.VA, seed.VB
		set[seed.A], set[seed.B] = true, true
		for d := range dims {
			if set[d] {
				continue
			}
			best, bestScore := "", -1
			for _, v := range dims[d].Values {
				score := 0
				for o := range dims {
					if !set[o] {
						continue
					}
					a, b, va, vb := o, d, row[o], v
					if a > b {
						a, b, va, vb = b, a, vb, va
					}
					if uncovered[Pair{A: a, B: b, VA: va, VB: vb}] {
						score++
					}
				}
				if score > bestScore {
					best, bestScore = v, score
				}
			}
			row[d], set[d] = best, true
		}
		if excluded == nil || !excluded(row) {
			return row
		}
		// Repair: try each value of each dimension the seed does not fix.
		if r := repair(dims, row, seed, excluded); r != nil {
			return r
		}
	}
	return nil
}

func repair(dims []Dimension, row []string, seed Pair, excluded func(row []string) bool) []string {
	for d := range dims {
		if d == seed.A || d == seed.B {
			continue
		}
		for _, v := range dims[d].Values {
			r := append([]string(nil), row...)
			r[d] = v
			if !excluded(r) {
				return r
			}
		}
	}
	return nil
}

// Covers reports the pairs of dims (except excluded ones) that rows miss.
func Covers(dims []Dimension, rows [][]string, excluded func(row []string) bool) []Pair {
	covered := map[Pair]bool{}
	for _, row := range rows {
		for a := 0; a < len(dims); a++ {
			for b := a + 1; b < len(dims); b++ {
				covered[Pair{A: a, B: b, VA: row[a], VB: row[b]}] = true
			}
		}
	}
	var missing []Pair
	for a := 0; a < len(dims); a++ {
		for b := a + 1; b < len(dims); b++ {
			for _, va := range dims[a].Values {
				for _, vb := range dims[b].Values {
					p := Pair{A: a, B: b, VA: va, VB: vb}
					if !covered[p] && !pairExcluded(dims, p, excluded) {
						missing = append(missing, p)
					}
				}
			}
		}
	}
	return missing
}
