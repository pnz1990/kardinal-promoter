// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// The outputs of a PR adoptMergedPR adopted (#2092): its head commit, the
// SCM account that opened it, as JSON its first maxAdoptedCommits commits
// kardinal did not push ([]scm.PRCommit), and how many there are. They are
// absent for a PR kardinal opened. Every value is cleaned (AdoptedPR) before
// it is written: the names are free text the SCM's users choose.
const (
	OutputPRAdoptedHead     = "prAdoptedHead"
	OutputPROpenedBy        = "prOpenedBy"
	OutputPRUnpushedCommits = "prUnpushedCommits"
	OutputPRUnpushedCount   = "prUnpushedCount"
)

const (
	// maxAdoptedCommits is the most unpushed commits listed; the count
	// stays exact.
	maxAdoptedCommits = 20
	// maxSCMName is the most bytes of an SCM-supplied name kept.
	maxSCMName = 128
	// maxUnreadable is the most bytes of an unreadable output echoed.
	maxUnreadable = 64
)

// InvalidSHA stands in for a commit ID that is not 7 to 64 hex digits.
const InvalidSHA = "(invalid)"

var (
	hexSHA = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	// emailPart is an e-mail address in angle brackets, as Bitbucket's
	// raw author ("Name <name@example.com>") carries it.
	emailPart = regexp.MustCompile(`\s*<[^<>]*>`)
)

// CleanSHA is sha, lowercased, when it is 7 to 64 hex digits, else
// InvalidSHA: what an SCM says is a commit ID is written only if it is one.
func CleanSHA(sha string) string {
	if s := strings.ToLower(sha); hexSHA.MatchString(s) {
		return s
	}
	return InvalidSHA
}

// CleanName is an SCM-supplied account or author name as kardinal records
// it: without an e-mail address in angle brackets, without control
// characters, and at most maxSCMName bytes (cut on a character boundary,
// marked with "…"). Renderings quote it (strconv.Quote), so no name can pass
// for the text around it.
func CleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, emailPart.ReplaceAllString(name, ""))
	name = strings.TrimSpace(name)
	if len(name) <= maxSCMName {
		return name
	}
	cut := maxSCMName - len("…")
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut] + "…"
}

// AdoptedPR is the cleaned record of an adopted PR, from the step's outputs.
type AdoptedPR struct {
	HeadSHA  string
	OpenedBy string
	// Unpushed is at most maxAdoptedCommits commits kardinal did not push;
	// UnpushedCount is how many there are.
	Unpushed      []scm.PRCommit
	UnpushedCount int
}

// adoptedOutputs are the outputs of an adopted PR, cleaned.
func adoptedOutputs(pr *scm.FoundPR) (map[string]string, error) {
	out := map[string]string{
		"prURL":             pr.URL,
		"prNumber":          fmt.Sprintf("%d", pr.Number),
		outputNoChanges:     "false",
		OutputPRAdoptedHead: CleanSHA(pr.HeadSHA),
		OutputPROpenedBy:    CleanName(pr.Author),
	}
	if len(pr.Unpushed) == 0 {
		return out, nil
	}
	list := make([]scm.PRCommit, 0, min(len(pr.Unpushed), maxAdoptedCommits))
	for _, c := range pr.Unpushed[:min(len(pr.Unpushed), maxAdoptedCommits)] {
		list = append(list, scm.PRCommit{SHA: CleanSHA(c.SHA), Author: CleanName(c.Author)})
	}
	b, err := json.Marshal(list)
	if err != nil {
		return nil, fmt.Errorf("record the adopted PR's commits: %w", err)
	}
	out[OutputPRUnpushedCommits] = string(b)
	out[OutputPRUnpushedCount] = strconv.Itoa(len(pr.Unpushed))
	return out, nil
}

// AdoptedPRRecord is the adopted PR in the step's outputs, cleaned again
// (CleanSHA, CleanName, the list bound) so that a reader never depends on
// the writer having done it; nil for a PR kardinal opened.
func AdoptedPRRecord(outputs map[string]string) *AdoptedPR {
	head, ok := outputs[OutputPRAdoptedHead]
	if !ok {
		return nil
	}
	a := &AdoptedPR{HeadSHA: CleanSHA(head), OpenedBy: CleanName(outputs[OutputPROpenedBy])}
	if raw := outputs[OutputPRUnpushedCommits]; raw != "" {
		var list []scm.PRCommit
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			// Not written by adoptMergedPR: say so rather than nothing.
			if len(raw) > maxUnreadable {
				raw = raw[:maxUnreadable]
			}
			list = []scm.PRCommit{{SHA: InvalidSHA, Author: "(unreadable: " + CleanName(raw) + ")"}}
		}
		for _, c := range list[:min(len(list), maxAdoptedCommits)] {
			a.Unpushed = append(a.Unpushed, scm.PRCommit{SHA: CleanSHA(c.SHA), Author: CleanName(c.Author)})
		}
	}
	a.UnpushedCount = len(a.Unpushed)
	if n, err := strconv.Atoi(outputs[OutputPRUnpushedCount]); err == nil && n > a.UnpushedCount {
		a.UnpushedCount = n
	}
	return a
}

// AdoptedPRDetail describes a PR adoptMergedPR adopted, from the step's
// outputs: `head <sha>, opened by "<account>"` and, when it has commits
// kardinal did not push, `, N commits not pushed by kardinal: <sha> by
// "<author>", ...` (the first maxAdoptedCommits, then "+M more"). Names are
// quoted and SHAs are hex or InvalidSHA, so SCM-supplied text cannot read as
// more of the record. "" for a PR kardinal opened.
func AdoptedPRDetail(outputs map[string]string) string {
	a := AdoptedPRRecord(outputs)
	if a == nil {
		return ""
	}
	d := "head " + a.HeadSHA + ", opened by " + quoteName(a.OpenedBy)
	if a.UnpushedCount == 0 {
		return d
	}
	parts := make([]string, 0, len(a.Unpushed)+1)
	for _, c := range a.Unpushed {
		parts = append(parts, c.SHA+" by "+quoteName(c.Author))
	}
	if more := a.UnpushedCount - len(a.Unpushed); more > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", more))
	}
	noun := "commits"
	if a.UnpushedCount == 1 {
		noun = "commit"
	}
	return fmt.Sprintf("%s, %d %s not pushed by kardinal: %s", d, a.UnpushedCount, noun, strings.Join(parts, ", "))
}

// quoteName is name quoted, or (unknown) when the SCM named no one.
func quoteName(name string) string {
	if name == "" {
		return "(unknown)"
	}
	return strconv.Quote(name)
}
