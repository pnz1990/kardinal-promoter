// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openvex/go-vex/pkg/vex"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Predicate types kardinal reads (docs/design/20-attestations.md §3).
const (
	PredicateSLSAProvenance = "https://slsa.dev/provenance/v1"
	PredicateSLSAVSA        = "https://slsa.dev/verification_summary/v1"
	PredicateCosignVuln     = "https://cosign.sigstore.dev/attestation/vuln/v1"
	PredicateInTotoVulns    = "https://in-toto.io/attestation/vulns"
	// PredicateOpenVEX is the default predicate type of VEX attestations.
	PredicateOpenVEX = "https://openvex.dev/ns"
)

// The bounds of what an attestation puts in the status (one etcd object).
const (
	// maxPredicateBytes is the largest predicate kept as canonical JSON.
	maxPredicateBytes = 64 << 10
	// maxPredicatesTotal is the most predicate JSON one status holds.
	maxPredicatesTotal = 256 << 10
	// maxEffectiveIDs bounds VulnsResult.EffectiveIDs.
	maxEffectiveIDs = 50
	// maxMessage bounds AttestationResult.Message.
	maxMessage = 1024
)

// verifiedStatement is one attestation bundle that verified: who signed
// it, when (its verified timestamp; zero when it has none), its place in
// the referrers, and its statement's predicate as canonical JSON.
type verifiedStatement struct {
	authority, signer, payloadDigest string
	at                               time.Time
	index                            int
	predicate                        []byte
}

// verifyStatements verifies bundles for the image digest against auths and
// returns those whose in-toto statement has predicateType exactly, newest
// last (by verified timestamp, then by place), and why the others did not
// verify.
func verifyStatements(digest string, bundles [][]byte, predicateType string, auths []authority) ([]verifiedStatement, []string) {
	raw, err := digestBytes(digest)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var out []verifiedStatement
	var reasons []string
	for i, b := range bundles {
		var bun bundle.Bundle
		if err := bun.UnmarshalJSON(b); err != nil {
			reasons = append(reasons, fmt.Sprintf("bundle %d: %v", i+1, err))
			continue
		}
		for _, a := range auths {
			res, signer, err := a.verifyBundle(&bun, raw)
			if err != nil {
				reasons = append(reasons, fmt.Sprintf("bundle %d, authority %s: %v", i+1, a.name, err))
				continue
			}
			if res.Statement == nil {
				reasons = append(reasons, fmt.Sprintf("bundle %d: not an in-toto statement", i+1))
				break
			}
			if res.Statement.PredicateType != predicateType {
				// Another attestation (or the signature): not a reason.
				break
			}
			vs := verifiedStatement{authority: a.name, signer: signer, index: i}
			for _, ts := range res.VerifiedTimestamps {
				if ts.Timestamp.After(vs.at) {
					vs.at = ts.Timestamp
				}
			}
			if env, err := bun.Envelope(); err == nil && env.RawEnvelope() != nil {
				sum := sha256.Sum256([]byte(env.RawEnvelope().Payload))
				vs.payloadDigest = "sha256:" + hex.EncodeToString(sum[:])
			}
			vs.predicate, err = canonicalPredicate(res.Statement.GetPredicate())
			if err != nil {
				reasons = append(reasons, fmt.Sprintf("bundle %d: predicate: %v", i+1, err))
				break
			}
			out = append(out, vs)
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.Before(out[j].at)
		}
		return out[i].index < out[j].index
	})
	return out, reasons
}

// canonicalPredicate is the predicate as JSON with sorted keys.
func canonicalPredicate(p *structpb.Struct) ([]byte, error) {
	if p == nil {
		return []byte("{}"), nil
	}
	raw, err := protojson.Marshal(p)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(raw)
}

// canonicalJSON re-encodes raw with sorted object keys (encoding/json sorts
// map keys).
func canonicalJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// namedAuthorities are the authorities of auths named in names.
func namedAuthorities(auths []authority, names []string) []authority {
	var out []authority
	for _, a := range auths {
		if slices.Contains(names, a.name) {
			out = append(out, a)
		}
	}
	return out
}

// evaluateAttestation is the result of policy p for image img, from the
// bundles its referrers hold: the verified attestation of the type that
// choose picks from them (newest last; nil picks the newest), with its SLSA
// level and vulnerability counts when the policy asks.
func evaluateAttestation(img v1alpha1.VerifiedImage, p v1alpha1.AttestationPolicy, bundles [][]byte, auths []authority,
	choose func([]verifiedStatement) int) v1alpha1.AttestationResult {
	res := v1alpha1.AttestationResult{Name: p.Name, Image: img.Repository + "@" + img.Digest, PredicateType: p.PredicateType}
	pa := namedAuthorities(auths, p.Authorities)
	found, reasons := verifyStatements(img.Digest, bundles, p.PredicateType, pa)
	if len(found) == 0 {
		res.Message = clip("no verified attestation of " + p.PredicateType + reasonSuffix(reasons))
		return res
	}
	pick := len(found) - 1
	if choose != nil {
		pick = choose(found)
	}
	newest := found[pick]
	res.Verified, res.Authority, res.Signer, res.Digest, res.Count = true, newest.authority, clip(newest.signer), newest.payloadDigest, len(found)
	if !newest.at.IsZero() {
		t := metav1.NewTime(newest.at.UTC())
		res.VerifiedAt = &t
	}
	res.Predicate = string(newest.predicate)
	if len(newest.predicate) > maxPredicateBytes {
		res.Predicate, res.PredicateOmitted = "", "64 KiB"
	}
	var notes []string
	if p.SLSA != nil {
		slsa, note := slsaResult(img, p, newest.predicate, bundles, pa)
		res.SLSA = slsa
		if note != "" {
			notes = append(notes, note)
		}
	}
	if p.PredicateType == PredicateCosignVuln || p.PredicateType == PredicateInTotoVulns {
		vulns, note := vulnsResult(img, p, newest.predicate, bundles, auths)
		res.Vulns = vulns
		if note != "" {
			notes = append(notes, note)
		}
	}
	res.Message = clip(strings.Join(notes, "; "))
	return res
}

// reasonSuffix is ": <reasons>" or "".
func reasonSuffix(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return ": " + strings.Join(reasons, "; ")
}

// clip bounds a status string.
func clip(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	return s[:maxMessage-3] + "..."
}

// slsaProvenance is the part of SLSA provenance v1 kardinal reads.
type slsaProvenance struct {
	BuildDefinition struct {
		ResolvedDependencies []struct {
			URI    string            `json:"uri"`
			Digest map[string]string `json:"digest"`
		} `json:"resolvedDependencies"`
	} `json:"buildDefinition"`
	RunDetails struct {
		Builder struct {
			ID string `json:"id"`
		} `json:"builder"`
	} `json:"runDetails"`
}

// slsaVSA is the part of a SLSA verification summary v1 kardinal reads.
type slsaVSA struct {
	ResourceURI        string   `json:"resourceUri"`
	VerificationResult string   `json:"verificationResult"`
	VerifiedLevels     []string `json:"verifiedLevels"`
}

// slsaResult decides the image's SLSA build level from the provenance
// (the level p grants its builder, 1 when not listed) or from a verified
// VSA signed by one of p's authorities that passed for this image, the
// higher of the two. note says what was ignored.
func slsaResult(img v1alpha1.VerifiedImage, p v1alpha1.AttestationPolicy, predicate []byte, bundles [][]byte,
	pa []authority) (*v1alpha1.SLSAResult, string) {
	out := &v1alpha1.SLSAResult{}
	if p.PredicateType == PredicateSLSAProvenance {
		var prov slsaProvenance
		if err := json.Unmarshal(predicate, &prov); err != nil {
			return out, "provenance: " + err.Error()
		}
		out.BuilderID = clipTo(prov.RunDetails.Builder.ID, 512)
		out.BuildLevel, out.LevelSource = 1, "builder"
		for _, b := range p.SLSA.Builders {
			if b.ID == prov.RunDetails.Builder.ID {
				out.BuildLevel = b.Level
			}
		}
		for _, d := range prov.BuildDefinition.ResolvedDependencies {
			if c := d.Digest["gitCommit"]; c != "" {
				out.SourceCommit, out.SourceURI = clipTo(c, 64), clipTo(d.URI, 512)
				break
			}
		}
	}
	vsaPredicates := [][]byte{}
	if p.PredicateType == PredicateSLSAVSA {
		vsaPredicates = append(vsaPredicates, predicate)
	} else {
		vsas, _ := verifyStatements(img.Digest, bundles, PredicateSLSAVSA, pa)
		for _, v := range vsas {
			vsaPredicates = append(vsaPredicates, v.predicate)
		}
	}
	var note string
	for _, raw := range vsaPredicates {
		var v slsaVSA
		if err := json.Unmarshal(raw, &v); err != nil {
			note = "VSA: " + err.Error()
			continue
		}
		if v.VerificationResult != "PASSED" {
			continue
		}
		if !resourceNamesImage(v.ResourceURI, img) {
			note = "a VSA for another resource (" + clipTo(v.ResourceURI, 200) + ") is ignored"
			continue
		}
		if l := vsaLevel(v.VerifiedLevels); l > out.BuildLevel {
			out.BuildLevel, out.LevelSource = l, "vsa"
		}
	}
	return out, note
}

// vsaLevel is the highest SLSA_BUILD_LEVEL_n of levels.
func vsaLevel(levels []string) int {
	best := 0
	for _, l := range levels {
		if n, err := strconv.Atoi(strings.TrimPrefix(l, "SLSA_BUILD_LEVEL_")); err == nil && strings.HasPrefix(l, "SLSA_BUILD_LEVEL_") &&
			n > best && n <= 3 {
			best = n
		}
	}
	return best
}

// resourceNamesImage reports whether a VSA's resourceUri names img: it ends
// in the image's digest ("<repo>@sha256:<hex>", a purl with the digest).
func resourceNamesImage(uri string, img v1alpha1.VerifiedImage) bool {
	hexPart := strings.TrimPrefix(img.Digest, "sha256:")
	for _, form := range []string{"@" + img.Digest, "@sha256%3A" + hexPart, "@sha256%3a" + hexPart} {
		if strings.Contains(uri, form) {
			return true
		}
	}
	return false
}

// clipTo bounds s to n bytes.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// finding is one vulnerability a scan reports.
type finding struct{ id, severity string }

// scanMeta is a scan's scanner and time.
type scanMeta struct{ scanner, version, scannedAt string }

// cosignVuln is the cosign vuln/v1 predicate (cosign attest --type vuln):
// the scanner's own report in scanner.result.
type cosignVuln struct {
	Scanner struct {
		URI     string          `json:"uri"`
		Version string          `json:"version"`
		Result  json.RawMessage `json:"result"`
	} `json:"scanner"`
	Metadata struct {
		ScanFinishedOn string `json:"scanFinishedOn"`
	} `json:"metadata"`
}

// inTotoVulns is the in-toto vulns v0.2 predicate.
type inTotoVulns struct {
	Scanner struct {
		URI     string `json:"uri"`
		Version string `json:"version"`
		Result  []struct {
			ID       string `json:"id"`
			Severity []struct {
				Method string `json:"method"`
				Score  string `json:"score"`
			} `json:"severity"`
		} `json:"result"`
	} `json:"scanner"`
	Metadata struct {
		ScanFinishedOn string `json:"scanFinishedOn"`
	} `json:"metadata"`
}

// parseScan reads the findings of a scan predicate: in-toto vulns, or
// cosign vuln holding a Trivy (Results[].Vulnerabilities[]) or Grype
// (matches[].vulnerability) report.
func parseScan(predicateType string, predicate []byte) ([]finding, scanMeta, error) {
	if predicateType == PredicateInTotoVulns {
		var p inTotoVulns
		if err := json.Unmarshal(predicate, &p); err != nil {
			return nil, scanMeta{}, err
		}
		meta := scanMeta{scanner: scannerName(p.Scanner.URI), version: p.Scanner.Version, scannedAt: p.Metadata.ScanFinishedOn}
		var out []finding
		for _, r := range p.Scanner.Result {
			sev := "unknown"
			for _, s := range r.Severity {
				if v := severityOf(s.Score); v != "unknown" {
					sev = v
					break
				}
			}
			out = append(out, finding{id: r.ID, severity: sev})
		}
		return out, meta, nil
	}
	var p cosignVuln
	if err := json.Unmarshal(predicate, &p); err != nil {
		return nil, scanMeta{}, err
	}
	meta := scanMeta{scanner: scannerName(p.Scanner.URI), version: p.Scanner.Version, scannedAt: p.Metadata.ScanFinishedOn}
	var report struct {
		Results []struct {
			Vulnerabilities []struct {
				VulnerabilityID string `json:"VulnerabilityID"`
				Severity        string `json:"Severity"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
		Matches []struct {
			Vulnerability struct {
				ID       string `json:"id"`
				Severity string `json:"severity"`
			} `json:"vulnerability"`
		} `json:"matches"`
	}
	if len(p.Scanner.Result) > 0 {
		if err := json.Unmarshal(p.Scanner.Result, &report); err != nil {
			return nil, meta, fmt.Errorf("scanner.result: %w", err)
		}
	}
	var out []finding
	for _, r := range report.Results {
		for _, v := range r.Vulnerabilities {
			out = append(out, finding{id: v.VulnerabilityID, severity: severityOf(v.Severity)})
		}
	}
	for _, m := range report.Matches {
		out = append(out, finding{id: m.Vulnerability.ID, severity: severityOf(m.Vulnerability.Severity)})
	}
	return out, meta, nil
}

// scannerName is the last element of a scanner URI ("pkg:github/aquasecurity/trivy@0.58.0" → trivy).
func scannerName(uri string) string {
	uri = strings.TrimSuffix(uri, "/")
	if i := strings.LastIndexAny(uri, "/:"); i >= 0 {
		uri = uri[i+1:]
	}
	if i := strings.Index(uri, "@"); i >= 0 {
		uri = uri[:i]
	}
	return clipTo(uri, 256)
}

// severityOf maps a severity word, or a CVSS base score, to critical,
// high, medium, low or unknown.
func severityOf(s string) string {
	switch w := strings.ToLower(strings.TrimSpace(s)); w {
	case "critical", "high", "medium", "low":
		return w
	case "moderate":
		return "medium"
	case "negligible":
		return "low"
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	switch {
	case err != nil || math.IsNaN(f):
		return "unknown"
	case f >= 9:
		return "critical"
	case f >= 7:
		return "high"
	case f >= 4:
		return "medium"
	case f > 0:
		return "low"
	}
	return "unknown"
}

// vexEntry is the latest VEX statement for one vulnerability of the image.
type vexEntry struct {
	status vex.Status
	at     time.Time
}

// vulnsResult counts a scan's findings by severity before and after VEX:
// a finding stays effective unless the latest OpenVEX statement for its ID
// (or an alias) and this image, from a verified VEX attestation of one of
// p.vex.authorities, says not_affected or fixed.
func vulnsResult(img v1alpha1.VerifiedImage, p v1alpha1.AttestationPolicy, predicate []byte, bundles [][]byte,
	auths []authority) (*v1alpha1.VulnsResult, string) {
	findings, meta, err := parseScan(p.PredicateType, predicate)
	out := &v1alpha1.VulnsResult{Scanner: meta.scanner, ScannerVersion: clipTo(meta.version, 64), ScannedAt: clipTo(meta.scannedAt, 40)}
	if err != nil {
		return out, "scan: " + err.Error()
	}
	var docs []vex.VEX
	var note string
	if p.VEX != nil {
		vexType := p.VEX.PredicateType
		if vexType == "" {
			vexType = PredicateOpenVEX
		}
		found, _ := verifyStatements(img.Digest, bundles, vexType, namedAuthorities(auths, p.VEX.Authorities))
		for _, f := range found {
			var d vex.VEX
			if err := json.Unmarshal(f.predicate, &d); err != nil {
				note = "VEX: " + err.Error()
				continue
			}
			out.VEXStatements += len(d.Statements)
			docs = append(docs, d)
		}
	}
	products := imageProducts(img)
	seen := map[string]bool{}
	for _, f := range findings {
		add(&out.Before, f.severity)
		if suppressed(docs, f.id, products) {
			continue
		}
		add(&out.Effective, f.severity)
		if (f.severity == "critical" || f.severity == "high") && !seen[f.id] && len(out.EffectiveIDs) < maxEffectiveIDs {
			seen[f.id] = true
			out.EffectiveIDs = append(out.EffectiveIDs, clipTo(f.id, 128))
		}
	}
	return out, note
}

// add counts one finding of severity.
func add(c *v1alpha1.SeverityCounts, severity string) {
	switch severity {
	case "critical":
		c.Critical++
	case "high":
		c.High++
	case "medium":
		c.Medium++
	case "low":
		c.Low++
	default:
		c.Unknown++
	}
}

// imageProducts are the identifiers an OpenVEX product may name the image
// by: repository@digest, the digest, its hex (a hash), and its pkg:oci purl.
func imageProducts(img v1alpha1.VerifiedImage) []string {
	hexPart := strings.TrimPrefix(img.Digest, "sha256:")
	repo := img.Repository
	nameOnly := repo[strings.LastIndex(repo, "/")+1:]
	base := strings.TrimSuffix(repo, "/"+nameOnly)
	purl := "pkg:oci/" + nameOnly + "@sha256%3A" + hexPart
	if base != repo {
		purl += "?repository_url=" + base + "/" + nameOnly
	}
	return []string{img.Repository + "@" + img.Digest, img.Digest, hexPart, purl, "pkg:oci/" + nameOnly + "@sha256%3A" + hexPart}
}

// suppressed reports whether the latest statement of docs for vulnerability
// id and one of the image's product identifiers is not_affected or fixed.
func suppressed(docs []vex.VEX, id string, products []string) bool {
	var latest *vexEntry
	for i := range docs {
		d := &docs[i]
		for _, prod := range products {
			for _, st := range d.Matches(id, prod, nil) {
				at := time.Time{}
				if st.Timestamp != nil {
					at = *st.Timestamp
				} else if d.Timestamp != nil {
					at = *d.Timestamp
				}
				if latest == nil || !at.Before(latest.at) {
					latest = &vexEntry{status: st.Status, at: at}
				}
			}
		}
	}
	return latest != nil && (latest.status == vex.StatusNotAffected || latest.status == vex.StatusFixed)
}

// boundPredicates drops predicates, the largest first, until the status
// holds at most maxPredicatesTotal bytes of them.
func boundPredicates(results []v1alpha1.AttestationResult) {
	total := 0
	for _, r := range results {
		total += len(r.Predicate)
	}
	for total > maxPredicatesTotal {
		largest := -1
		for i, r := range results {
			if r.Predicate != "" && (largest < 0 || len(r.Predicate) > len(results[largest].Predicate)) {
				largest = i
			}
		}
		if largest < 0 {
			return
		}
		total -= len(results[largest].Predicate)
		results[largest].Predicate, results[largest].PredicateOmitted = "", "256 KiB"
	}
}
