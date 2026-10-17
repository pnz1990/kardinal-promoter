# Design: Attestations, VEX and verifiable commit signatures

> Status: Accepted (spec for v0.13.0, AGENTIC-PLAN §4 "Evidence"). Implementation follows in a separate PR.
> Created: 2026-10-14
> Builds on: [image-verification.md](../image-verification.md) (the ImageVerification CR),
> [17-actors.md](17-actors.md) (asserted fields, AgentIdentity),
> [18-actor-policy.md](18-actor-policy.md) (gate CEL context).
> Consumers: 21 (promotion receipts record what this verifies), 22 (compliance mapping).
> Verified against: this repository at `417c94371`, and in the Go module cache:
> sigstore-go v1.3.0, in-toto/attestation v1.2.0, in-toto-golang v0.11.0, cosign/v3 v3.1.3,
> gitsign v0.17.1, go-vex v0.2.9, slsa-verifier/v2 v2.7.1, smimesign v0.2.0,
> go-containerregistry v0.22.1. Source references are `path:line` in those trees.

---

## 1. Problem

The ImageVerification CR proves that each image of a version is **signed** by an accepted
signer (`pkg/reconciler/imageverification/verify.go:94-128`), and that a config version's commit
carries a signature its SCM verified (`pkg/scm/commit_signature.go:79-113`). It says nothing
about **how** the image was built, **what** is in it, or **who** wrote a commit when the
signer is a workload, not a person with a key registered on the SCM.

The supply-chain questions an enterprise asks before prod are:

| Question | Evidence |
|----------|----------|
| Was this image built by our trusted builder, from this source? | SLSA provenance, with a build level |
| Does it pass our policy on any other claim (tests, SBOM, review)? | In-toto attestations with a known predicate type |
| Does it have known, exploitable vulnerabilities? | A vulnerability scan attestation, with VEX statements that say which findings do not apply |
| Who wrote the config commit, a person or agent X's workload? | A keyless (gitsign) commit signature, checked against a Fulcio identity |

This spec extends the ImageVerification reconciler to collect and verify those attestations,
and defines how gates decide on them, with CEL only, from status.

Out of scope: admission-time verification in the target cluster (policy-controller and Kyverno
remain the answer, `docs/image-verification.md` "What it cannot do"), signing (21), and SBOM
content queries beyond what a predicate exposes.

## 2. The model

The Pipeline's `spec.imageVerification` (an `ImageVerificationPolicy`,
`api/v1alpha1/imageverification_types.go`) gains `attestations`, and `commits` gains
`authorities`:

```yaml
spec:
  imageVerification:
    images: ["ghcr.io/myorg/*"]
    authorities:                      # image signatures, as today
      - name: release
        keyless: {issuer: https://token.actions.githubusercontent.com,
                  subjectRegExp: '^https://github\.com/myorg/[^/]+/\.github/workflows/release\.yml@refs/tags/.*$'}
    attestations:
      - name: provenance              # names the result; the gate reads attestations.provenance
        predicateType: https://slsa.dev/provenance/v1
        authorities: [release]        # names of spec.imageVerification.authorities that may sign it
        required: true                # missing after timeout: Failed. false: recorded when present
        slsa:
          builders:                   # builder.id -> the build level you grant it
            - id: https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v2.1.0
              level: 3
          minLevel: 2                 # below it: Failed
      - name: vulns
        predicateType: https://cosign.sigstore.dev/attestation/vuln/v1
        authorities: [release]
        required: true
        vex:                          # statements that say which findings do not apply
          authorities: [security]     # who may say so
          predicateType: https://openvex.dev/ns
      - name: tests
        predicateType: https://in-toto.io/attestation/test-result/v0.1
        authorities: [release]
    commits:
      requireSigned: true
      authorities:                    # new: keyless (gitsign) commit signers; when set, the SCM is not asked
        - name: agents
          keyless: {issuer: https://token.actions.githubusercontent.com,
                    subjectRegExp: '^https://github\.com/myorg/fixer/.*$'}
```

The reconciler verifies, the ImageVerification's status records the verified, bounded
results, and **gates decide** in PolicyGate CEL over `attestations.*` and `commit.*`
(section 6). `required` and `slsa.minLevel` are the only decisions the ImageVerification
makes itself: they hold the version's **first** environments, as signatures do today
(`pkg/graph/imageverify.go:221-225`). Everything environment-specific ("prod needs level 3 and
no critical vulnerability") is a gate.

## 3. Fetching and verifying attestations

### 3.1 Where attestations come from

The registry client already lists an image's OCI referrers and keeps Sigstore bundles
(`pkg/reconciler/imageverification/registry.go:121-131`, artifact type prefix
`application/vnd.dev.sigstore.bundle`, at most 32 per image of at most 1 MiB,
`registry.go:37-39`). An attestation made with `cosign attest` (v3, or v2
`--new-bundle-format`) is such a bundle whose DSSE envelope holds an in-toto statement with the
attestation's predicate type. The image signature itself is the special case with predicate
type `https://sigstore.dev/cosign/sign/v1` (`verify.go:90`, `cosignSignature` at `verify.go:180`).

| | Source | For | Against |
|-|--------|-----|---------|
| (a) | Sigstore bundles in OCI referrers only | One format, already fetched; the bundle carries the certificate, Rekor entry and timestamps; one verifier (sigstore-go) | Older `cosign attest` (`sha256-<digest>.att` tags) is not read |
| (b) | (a) plus legacy `.att` tags | Covers cosign v2 defaults | A second parser and verifier path (DSSE in an image layer, keyless certificate in an annotation), the same reason keyless legacy signatures are refused today (`verify.go:209-213`) |
| (c) | (a) plus a URL per attestation (Rekor search, an HTTP store) | Attestations not stored with the image | A network fetch to an arbitrary host, egress policy per Pipeline, no binding to the registry the image came from |

**Choice: (a).** The bundle format is what cosign v3 writes by default and what sigstore-go
verifies end to end. The raise of the minimum: the docs say how to re-attach with
`cosign attest --new-bundle-format`.

The per-image limit of 32 bundles now covers signatures and attestations together. It becomes
64, and the reconciler reads attestation bundles only for the predicate types the policy names.

### 3.2 Verification

sigstore-go already verifies a bundle and returns its in-toto statement:
`VerificationResult.Statement *in_toto.Statement` (`sigstore-go@v1.3.0/pkg/verify/signed_entity.go:253-258`,
the in-toto/attestation v1 Go type, `signed_entity.go:26`). The reconciler's `verifyEntity`
(`verify.go:133`) keeps its key and keyless paths. For an attestation:

1. The bundle verifies against one of the attestation's `authorities` (the same
   `SignatureAuthority` objects images use, so key, keyless and private trusted roots work as
   they do for signatures).
2. The statement's `subject` contains the image digest (`verify.WithArtifactDigest`, as today).
3. `statement.predicateType` equals the policy's `predicateType` exactly.
4. The predicate is decoded with its typed binding when kardinal knows the type (SLSA
   provenance v1, `in-toto/attestation@v1.2.0/go/predicates/provenance/v1/provenance.pb.go:31-34`;
   the VSA, `predicates/vsa/v1/vsa.pb.go:27-37`), or kept as JSON otherwise.

Several bundles with the same predicate type: the newest by Rekor integrated time that
verifies is used, and the count is recorded. One that does not verify is recorded with its
reason and never used.

Verified attestations are not checked again (Verified is terminal, as for signatures,
`imageverification_types.go` `ImageVerificationStatus.Phase`), except that a result past its
policy's `maxAge` is looked for again (section 5, #2044).

### 3.3 SLSA build level

The SLSA v1.0 build level is not a field of the provenance: it is a property of the builder
(how isolated it is, whether it signs provenance itself). A verifier assigns it.

| | How the level is decided | For | Against |
|-|-------------------------|-----|---------|
| (a) | The policy maps trusted `runDetails.builder.id` values to the level you grant them; provenance that verifies but names no listed builder is level 1 | Explicit and auditable; one CRD field; no new dependency | The policy author has to know the builder IDs |
| (b) | Use slsa-verifier as a library | Knows the official builders and their checks | +69 modules over kardinal's build, and pulls cosign/v2 and timestamp-authority with open advisories (section 9); builder-specific, GitHub-centric |
| (c) | Accept a SLSA VSA (`https://slsa.dev/verification_summary/v1`) from a trusted verifier and read its `verifiedLevels` | Delegates to the organisation's own verifier; no builder knowledge in kardinal | Needs that verifier to exist; a VSA alone says nothing about the source |

**Choice: (a), and (c) as an alternative source.** `slsa.builders[].level` (1 to 3) assigns
levels; `slsa.minLevel` holds the version below it. A VSA attestation (`predicateType`
`https://slsa.dev/verification_summary/v1`) whose `verificationResult` is `PASSED` and whose
`resourceUri` names the image raises the level to the highest `SLSA_BUILD_LEVEL_n` in
`verifiedLevels`, if its signer is an authority of the attestation. The status records which
source decided the level.

The provenance's source is recorded too: the `resolvedDependencies` entries with a
`gitCommit` digest (`provenance.pb.go:88`), so a gate can compare it with the version's
`bundle.provenance.commitSHA` (section 6).

### 3.4 Vulnerabilities and VEX

A vulnerability scan is an attestation like any other. The question is which format gives a
VEX-aware count.

| | Scan and VEX formats | For | Against |
|-|----------------------|-----|---------|
| (a) | Scans: cosign vuln (`https://cosign.sigstore.dev/attestation/vuln/v1`, what `cosign attest --type vuln` and Trivy write) and in-toto vulns v0.2 (`https://in-toto.io/attestation/vulns`). VEX: OpenVEX (`https://openvex.dev/ns`) | The formats Sigstore tooling writes today; OpenVEX has a small Go library (go-vex, +3 modules, `pkg/vex/vex.go:29`) and statuses that map one to one (`pkg/vex/status.go:13-23`) | CSAF and CycloneDX VEX producers need a converter (vexctl) |
| (b) | (a) plus CSAF VEX | Vendors publish CSAF | CSAF documents are large and product trees are hard to match to an image digest; go-vex's CSAF support is partial |
| (c) | CycloneDX VEX inside a CycloneDX SBOM attestation | One document for SBOM and VEX | Pulls an SBOM parser; VEX mixed with inventory; fewer producers attach it as an attestation |

**Choice: (a).** The reconciler computes the **effective** findings: every scan finding whose
vulnerability ID (or one of its aliases) is not covered by an OpenVEX statement with status
`not_affected` or `fixed` for this image (product `@id` matching the image digest or its
`pkg:oci/...` purl). `under_investigation` and `affected` keep the finding. A VEX statement
counts only from the attestation's `vex.authorities`, so a scanner cannot suppress its own
findings unless the policy says it may.

Recorded: counts by severity (critical, high, medium, low, unknown) before and after VEX, the
IDs of the effective critical and high findings (at most 50), and the scanner name, version and
database time. The scan's age is a gate decision (`attestations.vulns.scannedAt`).

### 3.5 Commit signatures with gitsign or cosign identities

Today a config commit is verified by asking the SCM whether *it* verified the signature
against a key its users registered (`commit_signature.go:79-113`). A workload, including an
agent's CI job, signs with gitsign: a CMS signature with a short-lived Fulcio certificate and a
Rekor entry embedded as a signed-data attribute (`gitsign@v0.17.1/internal/rekor/oid/oid.go:41`,
OID `1.3.6.1.4.1.57264.3.1`). SCMs do not verify those.

With `commits.authorities` set, the reconciler verifies the commit itself:

1. **Fetch the signed payload and signature.**

   | | How | For | Against |
   |-|-----|-----|---------|
   | (a) | The SCM API's commit verification object (GitHub `GET /repos/{o}/{r}/git/commits/{sha}` and Forgejo/Gitea `GET /api/v1/repos/{o}/{r}/git/commits/{sha}` return `verification.payload` and `verification.signature`) | Uses the providers kardinal already calls (`commit_signature.go:95, 137`); no git traffic | GitLab returns no raw payload; the response format is the provider's (recorded fixtures in tests) |
   | (b) | Fetch the commit object with git (`fetch --depth=1 <sha>`, then the raw object) | Works on every host; the bytes are what git hashed, so the SHA can be recomputed | A clone path in a reconciler that today makes only API calls; credentials for the config repository |
   | (c) | (a) where the API returns the payload, else (b) | Cheap where possible, complete everywhere | Two paths to test |

   **Choice: (c).** In both cases the reconciler recomputes the commit SHA from payload and
   signature (`gitsign pkg/git/verify.go:129-149`, `ObjectHashFromSignature`) and refuses a
   mismatch, so a provider cannot answer for another commit.
2. **Verify the CMS signature** over the payload against the Fulcio roots of the authority's
   trusted root (public-good through TUF, or `trustedRootRef`, as for images).
3. **Verify the embedded Rekor entry** offline, and take the signing time from its integrated
   time; the certificate must be valid at that time (gitsign's offline path,
   `gitsign pkg/rekor/rekor.go:281`).
4. **Match the certificate identity** (issuer, SAN) against the authorities' `keyless` fields.

The library choice is section 9. A commit signed with GPG or SSH under `commits.authorities`
fails: those stay with the SCM-verified path (`requireSigned` without `authorities`).

### 3.6 Making agent attribution verifiable

An actor stamp verifies who **created the Bundle** (17-actors §4); the commit it points at was
written by whoever pushed it. The commit author and any model or session an agent reports are
asserted (17-actors §4.1, `actor.asserted.*`).

`AgentIdentity` gains `spec.signingIdentities`: Fulcio identities (issuer, and subject or
subject regular expression) that are this agent's workload. When the verified signer of a
commit, or of a provenance attestation, matches exactly one ready AgentIdentity's signing
identity, the status records `agent: <name>`. A gate can then require that the agent that
created the version also signed its commit:

```cel
actor.kind != "agent" || commit.agent == actor.agent
```

| | Where the signer-to-agent mapping lives | For | Against |
|-|----------------------------------------|-----|---------|
| (a) | `AgentIdentity.spec.signingIdentities` | One registry for "who is this agent", kept by cluster admins; the reconciler already reads AgentIdentities (18-actor-policy §2.2) | An AgentIdentity CRD change |
| (b) | A map in each Pipeline's policy | Per-team | Any Pipeline owner could map any signer to any agent name |
| (c) | Asserted only: record the identity, let gates compare strings | No CRD change | No link to the registry; renames and suspensions are not seen |

**Choice: (a).** The mapping is the registry's, admins own it, and a suspended or not-ready
AgentIdentity maps to nothing.

## 4. Status: bounded, typed, and the predicate as data

The decoded predicate is untrusted input: it was signed by an accepted signer, but its content
is whatever the build wrote, and it lives in the ImageVerification's status, one etcd object
(at most 1.5 MiB).

| | How the predicate reaches the gate | For | Against |
|-|-----------------------------------|-----|---------|
| (a) | The reconciler writes a **typed summary** per attestation (SLSA level and builder, source commit, vulnerability counts) and the **predicate as canonical JSON**, capped per attestation and in total; the PolicyGate context reads them from the ImageVerification | CEL over any field (`json.unmarshal`, already in the PolicyGate environment, `cel_evaluator.go`), no network in the gate, every value written by the reconciler that verified it | A size cap; a predicate over the cap is not available to CEL |
| (b) | Evaluate per-attestation CEL in the ImageVerification reconciler and write pass/fail | No size problem | CEL outside `pkg/reconciler/policygate` is a Graph-first violation (AGENTS.md); conditions per environment would need several ImageVerifications |
| (c) | The PolicyGate reconciler fetches and verifies attestations itself | Always fresh | Network I/O in the gate's hot path (AGENTS.md anti-pattern); every gate re-verifies |
| (d) | One `AttestationRecord` CR per attestation | No total cap | More objects per version; a second watch; the CR still has the per-object limit |

**Choice: (a).** Caps: a predicate is kept when its canonical JSON is at most 64 KiB, and the
status holds at most 256 KiB of predicates in total (the larger ones are dropped first). A
dropped predicate keeps its typed summary and `predicateOmitted: "64 KiB"`; an expression that
reads `attestations.<name>.predicate` then fails, so the gate blocks rather than passes. The
typed fields are bounded by the schema (lengths, list sizes).

```yaml
status:
  attestations:
    - name: provenance
      predicateType: https://slsa.dev/provenance/v1
      verified: true
      signer: https://github.com/slsa-framework/slsa-github-generator/...@refs/tags/v2.1.0
      agent: ""                       # section 3.6
      digest: sha256:...              # of the DSSE payload; the receipt (21) records it
      slsa: {buildLevel: 3, levelSource: builder, builderID: https://github.com/..., sourceCommit: 1a2b..., sourceURI: git+https://github.com/myorg/app}
      predicate: '{"buildDefinition":...}'   # canonical JSON, at most 64 KiB
    - name: vulns
      verified: true
      vulns: {scanner: trivy, scannerVersion: 0.58.0, scannedAt: "2026-10-14T09:00:00Z",
              before: {critical: 2, high: 5}, effective: {critical: 0, high: 1},
              effectiveIDs: [CVE-2026-1234], vexStatements: 3}
  commit:
    verified: true
    method: gitsign                   # scm | gitsign
    signer: https://github.com/myorg/fixer/.github/workflows/fix.yml@refs/heads/main
    issuer: https://token.actions.githubusercontent.com
    agent: fixer
```

## 5. Gate timing

The ImageVerification holds the version's first environments until it is Verified (signatures,
`required` attestations, `minLevel`). Gates further along read its status, which is terminal by
then, except for attestation ages: with `attestations[].maxAge` (#2044) the reconciler writes each
result's `staleAt`, sets `stale` when it passes, and re-reads the image's referrers (a quarter of
maxAge, between 5 minutes and an hour) until a newer attestation replaces it; the phase stays
Verified and gates read `attestations.<name>.stale`. The clock stays in the reconciler's status
write (option (a) of #2044): no `now` reaches gate CEL, and in-flight versions are not re-verified
on an interval. A gate in an environment whose Pipeline has no attestation policy sees
`attestations.verified == false` and an empty map, so `attestations.provenance.slsa.buildLevel`
is an evaluation error and the gate blocks: fail closed, as for an unknown ChangeWindow.

## 6. CEL

The PolicyGate context (built in `buildContext`, `pkg/reconciler/policygate/reconciler.go:496`)
gains two variables, read from the version's ImageVerification (label `kardinal.io/bundle`,
`pkg/graph/imageverify.go:209-211`):

| Variable | Type | Value |
|----------|------|-------|
| `attestations.verified` | bool | the version's ImageVerification is Verified |
| `attestations.<name>.verified` | bool | that attestation verified |
| `attestations.<name>.signer`, `.agent`, `.predicateType` | string | from status |
| `attestations.<name>.slsa.buildLevel` | int | 0 to 3 |
| `attestations.<name>.slsa.builderID`, `.sourceCommit`, `.sourceURI` | string | from status |
| `attestations.<name>.effective.critical` (and `high`, `medium`, `low`, `unknown`) | int | a scan, after VEX |
| `attestations.<name>.before.*`, `.effectiveIDs`, `.scannedAt`, `.scanner` | int, list, string | a scan, from status |
| `attestations.<name>.predicate` | string | canonical JSON; use `json.unmarshal(...)` |
| `commit.verified`, `commit.method`, `commit.signer`, `commit.issuer`, `commit.agent` | bool, string | from status |

A scan's fields are on the attestation itself, so a policy that names its scan `vulns` reads
`attestations.vulns.effective.critical`. They are also under `attestations.<name>.vulns`, the
status field they come from (`attestations.vulns.vulns.effective.critical`). An earlier version of
this table wrote only that longer path, while the examples below and §3.4 used the shorter one.

Examples:

```cel
attestations.provenance.slsa.buildLevel >= 3
attestations.provenance.slsa.sourceCommit == bundle.provenance.commitSHA
attestations.vulns.effective.critical == 0 && attestations.vulns.effective.high <= 2
json.unmarshal(attestations.tests.predicate).result == "PASSED"
actor.kind != "agent" || commit.agent == actor.agent
```

A watch on ImageVerifications re-evaluates the gate instances of the same Bundle that read
`attestations.` or `commit.` (through `instanceGateRequests`, `pkg/reconciler/policygate/reconciler.go:1186`, as for MetricChecks).

## 7. Graph-first

| Item | Mechanism |
|------|-----------|
| Fetch and verify attestations and commit signatures | The ImageVerification reconciler, an owned node of the Bundle's Graph, writes its own status (network I/O stays in it, as for signatures today) |
| `required`, `minLevel` | The same status (`phase`), which the Graph already waits on (`imageVerifiedCond`, `pkg/graph/imageverify.go:221-225`) |
| Environment decisions | PolicyGate CEL over the ImageVerification's status: a cross-CRD read of the gate context, the ledger's G8 row "Cross-CRD reads for the gate context" gains ImageVerifications |
| Signer to agent | The reconciler reads AgentIdentities from the cache, like the PolicyGate reconciler (18-actor-policy §2.2) |

No kro gap: the Graph could read the ImageVerification through its existing node, but a gate
must decide with CEL over decoded JSON and record why, which is what the PolicyGate
reconciler is for (ledger G8, PolicyGate CEL row).

## 8. What this does not protect against

- **A trusted builder that lies.** The level is what the policy grants the builder; a
  compromised builder signs false provenance.
- **Stale scans.** A scan attests the database at `scannedAt`; a gate must bound its age.
  Vulnerabilities found later are not seen until a newer scan is attached; `maxAge` makes a gate
  require one (#2044).
- **VEX from the wrong party.** Only `vex.authorities` may suppress findings; listing the
  scanner's own identity there lets the build vouch for itself.
- **Oversized predicates.** CEL cannot read a predicate over the cap; split the claim into a
  smaller attestation or rely on the typed summary.
- **Unsigned commits pushed outside kardinal.** As today: verification covers the version's
  `configRef.commitSHA`, not every commit in the repository.
- **gitsign signatures without the embedded Rekor entry.** They are refused: verifying them
  needs an online Rekor search.

## 9. Libraries

Measured with a scratch module importing one package of each (`go list -deps`), against the 186
modules kardinal's build already uses; advisories with `govulncheck -scan package` (database of
2026-10-14), listing only findings in the library's own module tree that kardinal does not
already resolve at a fixed version (kardinal pins `golang.org/x/{crypto,net,mod,text}` and
grpc at fixed releases, and its CI runs govulncheck).

| Library (version) | Use | License | New modules over kardinal | Advisories of its own tree | Releases |
|-------------------|-----|---------|---------------------------|----------------------------|----------|
| sigstore-go v1.3.0 (`pkg/verify`, `pkg/bundle`, `pkg/tlog`) | Bundles, DSSE, statements, tlog | Apache-2.0 | 0 (in use) | none beyond shared x/* modules | v1.1.3 2025-09 to v1.3.0 2026-07, about monthly |
| in-toto/attestation v1.2.0 (`go/v1`, `go/predicates/...`) | Statement, SLSA v1, VSA typed bindings | Apache-2.0 | 0 (indirect today) | none | v1.1.0 2024-05, v1.2.0 2026-03 |
| in-toto-golang v0.11.0 (`in_toto`) | Layouts, links | Apache-2.0 | 0 (indirect) | none of its own | v0.9.0 2023-05 to v0.11.0 2026-05; its statement types are deprecated in favour of in-toto/attestation (`in_toto/attestations.go:32-36, 63`) |
| cosign/v3 v3.1.3 (`pkg/cosign`) | Everything cosign does | Apache-2.0 | 16 | none of its own | v3.0.5 2026-02 to v3.1.3 2026-08 |
| gitsign v0.17.1 (`pkg/git`, `pkg/rekor`) | gitsign commit verification | Apache-2.0 | 66 (go-git is already in kardinal; the rest is gitsign's CLI and cache stack) | go-git advisories fixed at v5.19.2, which kardinal already uses | v0.15.0 2026-05 to v0.17.1 2026-07 |
| github/smimesign v0.2.0 (`ietf-cms`) | CMS parsing alone | MIT | 1 | none | last release 2021-10; gitsign keeps its own fork (`gitsign internal/fork/ietf-cms`) |
| go-vex v0.2.9 (`pkg/vex`) | OpenVEX | Apache-2.0 | 3 | none | v0.2.5 2023-08 to v0.2.9 2026-09 |
| slsa-verifier/v2 v2.7.1 (`verifiers`) | SLSA builder checks | Apache-2.0 | 69 | cosign/v2 GO-2026-4309, GO-2026-4529, GO-2026-5694; timestamp-authority GO-2025-4192, GO-2026-5763, GO-2026-5851 (some without a fix) | — |
| go-containerregistry v0.22.1 (`v1/remote`) | Referrers | Apache-2.0 | 0 (in use) | none | — |

Decisions:

| Need | Options | Choice and why |
|------|---------|----------------|
| Bundles, attestations, statements | sigstore-go; cosign as a library; in-toto-golang | **sigstore-go**, already used: it returns the in-toto statement (`signed_entity.go:253-258`); cosign adds 16 modules for nothing more; in-toto-golang's statement types are deprecated |
| Typed predicates | in-toto/attestation bindings; hand-written structs; generic JSON | **in-toto/attestation**: already in the build, generated from the spec's protos |
| SLSA levels | slsa-verifier; policy builder map; VSA | **Policy map + VSA** (3.3): slsa-verifier costs 69 modules and pulls open advisories |
| VEX | go-vex; hand-written OpenVEX structs; CSAF library | **go-vex**: 3 modules, the reference implementation of OpenVEX |
| Commit signatures | gitsign `pkg/git` + `pkg/rekor`; smimesign `ietf-cms` + sigstore-go tlog; vendor gitsign's CMS fork | **gitsign**, accepted for its size: the only maintained implementation of exactly this check, kept current with gitsign's signature changes; smimesign is unmaintained since 2021, and vendoring a fork makes kardinal its maintainer. The implementation PR is a go.mod change and goes through review as such |

## 10. Upgrade from v0.12

- Every new field is optional. A Pipeline without `attestations` or `commits.authorities`
  verifies exactly as before; its ImageVerification spec, and so its name hash, is unchanged.
- The CRDs (`imageverifications`, `pipelines`, `agentidentities`) are applied before the chart.
- `maxSignatures` rising from 32 to 64 changes nothing for existing results.
- `attestations.*` and `commit.*` read as unverified for versions verified before the upgrade.

## 11. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| C1 | Where attestations come from | OCI referrer bundles; + legacy `.att`; + URL stores | **Referrer bundles** (3.1): one verified format |
| C2 | SLSA build level | policy builder map; slsa-verifier; VSA | **Builder map, VSA as a second source** (3.3) |
| C3 | Scan and VEX formats | cosign vuln + in-toto vulns + OpenVEX; + CSAF; CycloneDX | **cosign vuln, in-toto vulns, OpenVEX** (3.4) |
| C4 | Who may suppress a finding | anyone who signed a VEX; the scan's signer; `vex.authorities` | **`vex.authorities`** (3.4): a scanner does not vouch for itself unless listed |
| C5 | Commit payload | SCM API; git object fetch; both | **Both**, the API first (3.5), SHA recomputed |
| C6 | Signer to agent | AgentIdentity `signingIdentities`; per-Pipeline map; asserted string | **AgentIdentity** (3.6) |
| C7 | How the predicate reaches CEL | bounded status + gate CEL; CEL in the IV reconciler; gate fetches; one CR per attestation | **Bounded status, CEL in the gate** (4) |
| C8 | Libraries | section 9 | **sigstore-go, in-toto/attestation, go-vex, gitsign** |

## 12. Test plan

**Unit** (table-driven, `-race`):

- Verification: an attestation bundle for the right digest, the wrong digest, another
  predicate type, an untrusted signer, a key and a keyless authority, a private trusted root.
- SLSA: listed builder at each level, unlisted builder (level 1), `minLevel` holding, a VSA
  raising the level, a VSA from an untrusted signer ignored, a VSA for another resource.
- VEX: `not_affected` and `fixed` suppress, `affected` and `under_investigation` keep, aliases,
  purl and digest product matching, VEX from a non-authority ignored, effective counts.
- Commits: gitsign signature verifies, SHA mismatch refused, expired certificate at the Rekor
  time, missing embedded entry refused, GPG under `authorities` refused, signer to agent
  mapping (one match, none, two, a suspended agent).
- Bounds: predicate at 64 KiB and above, total over 256 KiB drops the largest, the CEL error
  when an omitted predicate is read.
- CEL: every documented attribute, held to `docs/reference/cel-context.md` by
  `TestDocumentedCELContext`.

**Fake-client journeys:** a version with level-3 provenance reaches prod while one with level 2
is held at the prod gate; critical findings suppressed by VEX let a version through, and the
same findings without VEX hold it; an agent's version whose commit was signed by its own
workload passes `commit.agent == actor.agent`, another agent's signature holds it.

**Live e2e** (core suite, `count=2` in CI, with `coverage.tsv` rows): the in-cluster OCI
registry of the suite gets cosign-attested images (key authority, and a keyless one with the
suite's private Sigstore if present), provenance and vuln attestations made with `cosign attest
--new-bundle-format`, and an OpenVEX attestation; a Forgejo repository gets a commit signed with a
gitsign-style CMS signature from a test Fulcio; the gates above run end to end.
