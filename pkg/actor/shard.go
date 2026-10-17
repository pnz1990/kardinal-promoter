// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"fmt"
	"strings"
)

// The agent index is split into shards by principal (#2071): the API server
// converts the whole param of an admission policy on every request, so one
// index made every checked write cost time linear in the cluster's
// AgentIdentities. Each principal is in exactly one shard, so a principal
// absent from its shard is not an agent, as it was for the single index.
const (
	// DefaultIndexShards is actors.index.shards' default.
	DefaultIndexShards = 8
	// MaxIndexShards bounds actors.index.shards (1 to 64).
	MaxIndexShards = 64
)

// shardAlphabet gives each character its weight (its position + 1);
// a character not in it weighs 0.
const shardAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-.:@_"

// shardFront and shardBack are how many characters from the start and from
// the end of a principal are weighed by position. A principal's characters
// are also summed whole, so a name that differs in the middle or at either
// end lands in a shard of its own about as often as any other
// (TestShardOf_Balance).
const (
	shardFront = 64
	shardBack  = 8
)

func shardWeight(r rune) int { return strings.IndexRune(shardAlphabet, r) + 1 }

func frontWeight(j int) int { return (j*j)%97 + 1 }

func backWeight(j int) int { return j*j + 1 }

// ShardOf is the shard (0 to shards-1) of the agent index that holds
// principal's entry. ShardExpression is the same function in CEL, which the
// chart's actor policies route with; the two are pinned by
// TestShardExpression_MatchesShardOf. It counts in code points, as CEL's
// size() and charAt do.
func ShardOf(principal string, shards int) int {
	if shards <= 1 {
		return 0
	}
	u := []rune(principal)
	sum, front, back := 0, 0, 0
	for _, r := range u {
		sum += shardWeight(r)
	}
	for j := 0; j < shardFront && j < len(u); j++ {
		front += shardWeight(u[j]) * frontWeight(j)
	}
	for j := 0; j < shardBack && j < len(u); j++ {
		back += shardWeight(u[len(u)-1-j]) * backWeight(j)
	}
	return (sum*7 + back*13 + front*5 + len(u)*3) % shards
}

// ShardExpression is ShardOf in CEL, for the string expression u (for
// example request.userInfo.username), with the Kubernetes CEL libraries
// (strings v2: split, charAt, indexOf; lists: sum). The chart's actor
// policies route a request to its shard's policy with it.
func ShardExpression(u string, shards int) string {
	if shards <= 1 {
		return "0"
	}
	w := func(c string) string { return fmt.Sprintf("('%s'.indexOf(%s) + 1)", shardAlphabet, c) }
	var front, back []string
	for j := 0; j < shardFront; j++ {
		front = append(front, fmt.Sprintf("(size(%s) > %d ? %s * %d : 0)", u, j, w(fmt.Sprintf("%s.charAt(%d)", u, j)), frontWeight(j)))
	}
	for j := 0; j < shardBack; j++ {
		back = append(back, fmt.Sprintf("(size(%s) > %d ? %s * %d : 0)", u, j, w(fmt.Sprintf("%s.charAt(size(%s) - %d)", u, u, j+1)), backWeight(j)))
	}
	sum := fmt.Sprintf("%s.split('').map(c, %s).sum()", u, w("c"))
	return fmt.Sprintf("((%s) * 7 + (%s) * 13 + (%s) * 5 + size(%s) * 3) %% %d",
		sum, strings.Join(back, " + "), strings.Join(front, " + "), u, shards)
}

// ShardName is the name of shard i of the agent index named index.
func ShardName(index string, i int) string { return fmt.Sprintf("%s-shard-%d", index, i) }
