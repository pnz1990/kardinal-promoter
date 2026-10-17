// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImageVerification phases. Verified and Failed are terminal.
const (
	ImageVerificationPending  = "Pending"
	ImageVerificationVerified = "Verified"
	ImageVerificationFailed   = "Failed"
)

// ImageVerificationPolicy is a Pipeline's signature policy: which Bundle
// images must carry a valid signature, from whom, and whether a config
// Bundle's commit must be signed. A Bundle the policy applies to is not
// promoted into its first environments until the ImageVerification of its
// Graph is Verified. See docs/image-verification.md.
// +kubebuilder:validation:XValidation:rule="(has(self.authorities) && size(self.authorities) > 0) || (has(self.commits) && self.commits.requireSigned)",message="set authorities, commits.requireSigned, or both"
// +kubebuilder:validation:XValidation:rule="!has(self.attestations) || self.attestations.all(a, a.authorities.all(n, has(self.authorities) && self.authorities.exists(x, x.name == n)) && (!has(a.vex) || a.vex.authorities.all(n, has(self.authorities) && self.authorities.exists(x, x.name == n))))",message="an attestation's authorities and vex.authorities must name spec.imageVerification.authorities"
type ImageVerificationPolicy struct {
	// Images selects the Bundle images to verify by repository, with "*"
	// matching any characters ("ghcr.io/myorg/*"). Empty means every image.
	// Every selected image must be pinned by digest in the Bundle.
	// +kubebuilder:validation:MaxItems=20
	// +optional
	Images []string `json:"images,omitempty"`

	// Authorities are the signers an image may be signed by: an image is
	// verified when one of its signatures verifies against any authority.
	// +kubebuilder:validation:MaxItems=10
	// +listType=map
	// +listMapKey=name
	// +optional
	Authorities []SignatureAuthority `json:"authorities,omitempty"`

	// Commits, when requireSigned is true, requires the commit of a config or
	// mixed Bundle (spec.configRef.commitSHA) to be signed, as the SCM
	// provider reports it (GitHub, GitLab, Forgejo, Gitea).
	// +optional
	Commits *CommitSignaturePolicy `json:"commits,omitempty"`

	// RegistrySecretRef names a kubernetes.io/dockerconfigjson Secret in the
	// Pipeline namespace with credentials for the registries holding the
	// images and their signatures. Without it registries are read
	// anonymously.
	// +optional
	RegistrySecretRef *LocalObjectName `json:"registrySecretRef,omitempty"`

	// SignatureRepository is the repository that holds the signatures, when
	// they are not stored next to the images (cosign's COSIGN_REPOSITORY),
	// for example "registry.example.com/signatures". Signatures are looked
	// up there by image digest.
	// +optional
	SignatureRepository string `json:"signatureRepository,omitempty"`

	// InsecureRegistries are registry hosts ("registry.local:5000") read over
	// plain HTTP. Every other registry is read over HTTPS.
	// +kubebuilder:validation:MaxItems=10
	// +optional
	InsecureRegistries []string `json:"insecureRegistries,omitempty"`

	// Attestations are the in-toto attestations each verified image must (or
	// may) carry, as Sigstore bundles in its OCI referrers: SLSA provenance,
	// vulnerability scans with VEX, and any other predicate. Each result is
	// recorded in status.attestations; PolicyGate CEL reads it as
	// attestations.<name> (docs/image-verification.md#attestations).
	// +kubebuilder:validation:MaxItems=10
	// +listType=map
	// +listMapKey=name
	// +optional
	Attestations []AttestationPolicy `json:"attestations,omitempty"`

	// Timeout bounds how long a missing signature is waited for (CI may sign
	// after it pushes), from the moment the ImageVerification starts. A
	// signature that does not verify fails at once. Empty or "0" means 10m.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// AttestationPolicy is one attestation an image carries: its predicate
// type, who may sign it, and what the ImageVerification decides itself
// (required, slsa.minLevel). Everything else is a gate's decision.
// +kubebuilder:validation:XValidation:rule="!has(self.vex) || self.predicateType in ['https://cosign.sigstore.dev/attestation/vuln/v1', 'https://in-toto.io/attestation/vulns']",message="vex applies to a vulnerability scan (cosign vuln or in-toto vulns predicate)"
// +kubebuilder:validation:XValidation:rule="!has(self.slsa) || self.predicateType in ['https://slsa.dev/provenance/v1', 'https://slsa.dev/verification_summary/v1']",message="slsa applies to SLSA provenance or a SLSA VSA"
type AttestationPolicy struct {
	// Name names the result: a gate reads attestations.<name>.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9]*$`
	Name string `json:"name"`

	// PredicateType is the in-toto predicate type, exactly
	// ("https://slsa.dev/provenance/v1").
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	PredicateType string `json:"predicateType"`

	// Authorities name the spec.imageVerification.authorities that may sign
	// the attestation.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=63
	Authorities []string `json:"authorities"`

	// Required holds the version until every image carries a verified
	// attestation of this type, and fails it at the timeout. False records
	// the attestation when it is there.
	// +optional
	Required bool `json:"required,omitempty"`

	// SLSA grants SLSA build levels to provenance builders, and holds a
	// version below MinLevel.
	// +optional
	SLSA *SLSAPolicy `json:"slsa,omitempty"`

	// VEX names who may say, with OpenVEX statements, that a finding of the
	// scan does not apply.
	// +optional
	VEX *VEXPolicy `json:"vex,omitempty"`

	// MaxAge, when set, is how old the attestation may be (a Go duration,
	// for example 24h): a scan from its scannedAt, any other attestation
	// from its verified signing time. Past it the result is stale
	// (status.attestations[].stale, attestations.<name>.stale in a gate),
	// also after the ImageVerification is Verified, and kardinal looks for
	// a newer attestation in the image's referrers until one replaces it.
	// A required attestation that is already stale is waited for like a
	// missing one. An attestation with no verified time (a key signature
	// without a transparency log entry) is stale at once. See
	// docs/image-verification.md#attestation-age.
	// +kubebuilder:validation:Pattern=`^(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+$`
	// +optional
	MaxAge string `json:"maxAge,omitempty"`
}

// SLSAPolicy is how SLSA build levels are decided: the level a policy
// grants a provenance builder (runDetails.builder.id), or a SLSA
// verification summary (VSA) from one of the attestation's authorities.
type SLSAPolicy struct {
	// Builders map trusted builder IDs to the level granted to them.
	// Provenance from a builder not listed is level 1.
	// +kubebuilder:validation:MaxItems=20
	// +optional
	Builders []SLSABuilder `json:"builders,omitempty"`

	// MinLevel holds the version (Failed at the timeout) while an image's
	// level is below it. 0 holds nothing.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3
	// +optional
	MinLevel int `json:"minLevel,omitempty"`
}

// SLSABuilder is a trusted builder and the level granted to it.
type SLSABuilder struct {
	// ID is the builder's runDetails.builder.id, exactly.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	ID string `json:"id"`

	// Level is the SLSA build level granted (1 to 3).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3
	Level int `json:"level"`
}

// VEXPolicy is who may suppress findings of a vulnerability scan.
type VEXPolicy struct {
	// Authorities name the spec.imageVerification.authorities whose OpenVEX
	// attestations count. A scan's own signer does not suppress its
	// findings unless it is listed here.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=63
	Authorities []string `json:"authorities"`

	// PredicateType is the predicate type of the VEX attestations. Empty
	// means https://openvex.dev/ns.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	PredicateType string `json:"predicateType,omitempty"`
}

// SignatureAuthority is one accepted signer: a public key, or a keyless
// (Sigstore Fulcio) identity. Exactly one of key and keyless is set.
// +kubebuilder:validation:XValidation:rule="has(self.key) != has(self.keyless)",message="set exactly one of key and keyless"
type SignatureAuthority struct {
	// Name identifies the authority in results.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Key is a PEM public key (cosign.pub) in a Secret of the Pipeline
	// namespace.
	// +optional
	Key *KeyAuthority `json:"key,omitempty"`

	// Keyless is a Sigstore keyless identity: the OIDC issuer and subject
	// of the Fulcio certificate that signed.
	// +optional
	Keyless *KeylessAuthority `json:"keyless,omitempty"`
}

// KeyAuthority is a public key a signature must verify against.
type KeyAuthority struct {
	// SecretRef names the Secret and key holding the PEM public key.
	SecretRef SecretKeyName `json:"secretRef"`

	// RequireTransparencyLog requires a Rekor entry (and its inclusion
	// proof) in the Sigstore bundle. Default false: a key signature is
	// verified against the key alone, like cosign --insecure-ignore-tlog.
	// +optional
	RequireTransparencyLog bool `json:"requireTransparencyLog,omitempty"`
}

// KeylessAuthority is a Fulcio certificate identity.
// +kubebuilder:validation:XValidation:rule="has(self.subject) != has(self.subjectRegExp)",message="set exactly one of subject and subjectRegExp"
type KeylessAuthority struct {
	// Issuer is the OIDC issuer, for example
	// https://token.actions.githubusercontent.com.
	// +kubebuilder:validation:MinLength=1
	Issuer string `json:"issuer"`

	// Subject is the certificate's subject (SAN), for example
	// https://github.com/myorg/app/.github/workflows/release.yml@refs/heads/main.
	// +optional
	Subject string `json:"subject,omitempty"`

	// SubjectRegExp is a regular expression the subject must match.
	// +optional
	SubjectRegExp string `json:"subjectRegExp,omitempty"`

	// TrustedRootRef names a Secret key in the Pipeline namespace holding a
	// Sigstore trusted_root.json (a private Sigstore). Empty means the
	// Sigstore public-good instance, whose trusted root the controller
	// fetches through TUF.
	// +optional
	TrustedRootRef *SecretKeyName `json:"trustedRootRef,omitempty"`
}

// CommitSignaturePolicy requires signed commits.
type CommitSignaturePolicy struct {
	// RequireSigned requires the config Bundle's commit to be signed with a
	// signature the SCM provider verified. configRef.commitSHA must then be
	// a full 40- or 64-character SHA, and configRef.gitRepo must be on the
	// controller's SCM host.
	RequireSigned bool `json:"requireSigned"`

	// AllowedSigners, when set, also requires the signer to be one of these:
	// an SCM login or an email address, as the SCM reports the verified
	// signer (GitLab: the key owner's verified email, the committer's for
	// SSH; never a key title). Commits the SCM platform signed itself, not a
	// person, are refused unless listed here explicitly: "web-flow" (GitHub
	// web UI edits and merges), "gitlab-system" (GitLab verified_system) and
	// "forgejo-instance" (the Forgejo/Gitea instance key). To accept PRs
	// merged in the web UI, list the platform identity. Empty means any
	// person's signature the SCM verified.
	// +kubebuilder:validation:MaxItems=50
	// +optional
	AllowedSigners []string `json:"allowedSigners,omitempty"`
}

// LocalObjectName names an object in the same namespace.
type LocalObjectName struct {
	// Name is the object name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SecretKeyName names one key of a Secret in the same namespace.
type SecretKeyName struct {
	// Name is the object name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Key is the data key.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// VerifiedImage is one image an ImageVerification checks.
type VerifiedImage struct {
	// Repository is the image repository, without tag or digest.
	Repository string `json:"repository"`
	// Digest is the sha256 digest the Bundle pins.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	Digest string `json:"digest"`
}

// VerifiedCommit is the commit an ImageVerification checks.
type VerifiedCommit struct {
	// Repo is the git repository URL.
	Repo string `json:"repo"`
	// SHA is the commit.
	SHA string `json:"sha"`
	// ScmProvider is the provider of the Pipeline's spec.git.providerRef,
	// as the translator resolved it: the signature is checked with that
	// provider (its token, host and allowedRepositories). Unset, the
	// controller's --scm-provider checks it.
	// +optional
	ScmProvider *ScmProviderIdentity `json:"scmProvider,omitempty"`
}

// ImageVerificationSpec is what one Bundle must prove before its first
// environments are promoted. The Bundle's Graph creates it from the
// Pipeline's spec.imageVerification and the Bundle's images. It is
// immutable: a policy change gives a new ImageVerification (its name carries
// a hash of the spec), so a verdict is never reused for another policy.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="an ImageVerification's spec is immutable"
type ImageVerificationSpec struct {
	// PipelineName is the Pipeline.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`
	// BundleName is the Bundle.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`
	// Images are the images to verify, pinned by digest.
	// +optional
	Images []VerifiedImage `json:"images,omitempty"`
	// Commit is the config commit to verify, when commits.requireSigned.
	// +optional
	Commit *VerifiedCommit `json:"commit,omitempty"`
	// Policy is the Pipeline's policy (its images selector already applied).
	Policy ImageVerificationPolicy `json:"policy"`
}

// ImageVerificationResult is the result for one image.
type ImageVerificationResult struct {
	// Image is "repository@digest".
	Image string `json:"image"`
	// Verified is true once a signature verified.
	Verified bool `json:"verified"`
	// Authority is the authority that verified it.
	// +optional
	Authority string `json:"authority,omitempty"`
	// Signer is the key's authority name or the certificate identity.
	// +optional
	Signer string `json:"signer,omitempty"`
	// Message says why the image is not verified yet, or failed.
	// +optional
	Message string `json:"message,omitempty"`
}

// CommitVerificationResult is the result for the config commit.
type CommitVerificationResult struct {
	// Verified is true when the SCM verified the commit's signature.
	Verified bool `json:"verified"`
	// Signer is who signed, as the SCM reports it.
	// +optional
	Signer string `json:"signer,omitempty"`
	// Message is the SCM's reason when not verified.
	// +optional
	Message string `json:"message,omitempty"`
}

// AttestationResult is one attestation of one image: whether it verified,
// who signed it, and its typed summary and predicate.
type AttestationResult struct {
	// Name is the policy's attestations[].name.
	Name string `json:"name"`
	// Image is "repository@digest".
	Image string `json:"image"`
	// PredicateType is the policy's predicate type.
	// +kubebuilder:validation:MaxLength=256
	PredicateType string `json:"predicateType"`
	// Verified is true when an attestation of the type, for the image,
	// verified against one of the policy's authorities.
	Verified bool `json:"verified"`
	// Authority is the authority that verified it.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Authority string `json:"authority,omitempty"`
	// Signer is the key's authority name or the certificate identity.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Signer string `json:"signer,omitempty"`
	// VerifiedAt is the verified signing time of the attestation: its
	// transparency log or timestamp authority time.
	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`
	// DatedAt is when kardinal first recorded this attestation (its
	// digest): the age base never counts from later than this, so a scan
	// that claims a future scannedAt ages from when it was first seen.
	// +optional
	DatedAt *metav1.Time `json:"datedAt,omitempty"`
	// StaleAt is when the attestation passes the policy's maxAge.
	// +optional
	StaleAt *metav1.Time `json:"staleAt,omitempty"`
	// Stale is true once StaleAt has passed, until a newer attestation
	// replaces it.
	// +optional
	Stale bool `json:"stale,omitempty"`
	// Digest is the sha256 of the verified DSSE payload.
	// +kubebuilder:validation:MaxLength=80
	// +optional
	Digest string `json:"digest,omitempty"`
	// Count is how many attestations of the type verified; the newest is used.
	// +optional
	Count int `json:"count,omitempty"`
	// Message says why it is not verified, or what was dropped.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Message string `json:"message,omitempty"`
	// SLSA is the provenance summary, for a policy with slsa.
	// +optional
	SLSA *SLSAResult `json:"slsa,omitempty"`
	// Vulns is the scan summary, for a vulnerability scan predicate.
	// +optional
	Vulns *VulnsResult `json:"vulns,omitempty"`
	// Predicate is the predicate as canonical JSON (sorted keys), at most
	// 64 KiB; empty when omitted (PredicateOmitted).
	// +kubebuilder:validation:MaxLength=65536
	// +optional
	Predicate string `json:"predicate,omitempty"`
	// PredicateOmitted says why Predicate is empty although the attestation
	// verified: "64 KiB" (over the per-attestation cap) or "256 KiB" (over
	// the status total, the largest dropped first).
	// +kubebuilder:validation:MaxLength=16
	// +optional
	PredicateOmitted string `json:"predicateOmitted,omitempty"`
}

// SLSAResult is what the provenance says, and the level decided.
type SLSAResult struct {
	// BuildLevel is the SLSA build level (0 to 3): the level the policy
	// grants the builder (1 when it is not listed), or a verified VSA's.
	BuildLevel int `json:"buildLevel"`
	// LevelSource is builder or vsa.
	// +kubebuilder:validation:MaxLength=16
	// +optional
	LevelSource string `json:"levelSource,omitempty"`
	// BuilderID is runDetails.builder.id.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	BuilderID string `json:"builderID,omitempty"`
	// SourceCommit is the gitCommit digest of the first resolved dependency
	// that has one.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	SourceCommit string `json:"sourceCommit,omitempty"`
	// SourceURI is that dependency's URI.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	SourceURI string `json:"sourceURI,omitempty"`
}

// VulnsResult is a scan's findings by severity, before and after VEX.
type VulnsResult struct {
	// Scanner is the scanner's name (its URI's last element).
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Scanner string `json:"scanner,omitempty"`
	// ScannerVersion is the scanner's version.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ScannerVersion string `json:"scannerVersion,omitempty"`
	// ScannedAt is when the scan finished (RFC 3339).
	// +kubebuilder:validation:MaxLength=40
	// +optional
	ScannedAt string `json:"scannedAt,omitempty"`
	// Before counts every finding.
	Before SeverityCounts `json:"before"`
	// Effective counts the findings no VEX statement covers.
	Effective SeverityCounts `json:"effective"`
	// EffectiveIDs are the IDs of the effective critical and high findings
	// (at most 50).
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=128
	// +optional
	EffectiveIDs []string `json:"effectiveIDs,omitempty"`
	// VEXStatements counts the statements read from verified VEX
	// attestations.
	// +optional
	VEXStatements int `json:"vexStatements,omitempty"`
}

// SeverityCounts counts findings by severity.
type SeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// ImageVerificationStatus is the observed state.
type ImageVerificationStatus struct {
	// Phase is Pending, Verified or Failed. Verified and Failed are terminal:
	// a verified signature is not checked again (revocation after the
	// verification is not seen).
	// +kubebuilder:validation:Enum=Pending;Verified;Failed
	// +kubebuilder:validation:XValidation:rule="!(oldSelf in ['Verified', 'Failed']) || self == oldSelf",message="a finished ImageVerification's phase cannot change"
	// +optional
	Phase string `json:"phase,omitempty"`
	// Message summarizes the phase.
	// +optional
	Message string `json:"message,omitempty"`
	// Reason is a machine-readable reason for a Failed or waiting
	// verification: SecretNotReferenceable, SecretNotFound,
	// InvalidPublicKey, InvalidTrustedRoot, InvalidRegistryCredentials,
	// SignatureNotVerified, CommitNotVerified, AttestationNotVerified,
	// SLSALevelTooLow, Timeout.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Images has one result per spec.images entry.
	// +optional
	Images []ImageVerificationResult `json:"images,omitempty"`
	// Commit is the commit result.
	// +optional
	Commit *CommitVerificationResult `json:"commit,omitempty"`
	// Attestations has one result per attestation policy and image.
	// +kubebuilder:validation:MaxItems=200
	// +optional
	Attestations []AttestationResult `json:"attestations,omitempty"`
	// StartedAt is when the verification started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// Deadline is StartedAt plus the policy timeout.
	// +optional
	Deadline *metav1.Time `json:"deadline,omitempty"`
	// LastCheckedAt is when the registry or SCM was last asked.
	// +optional
	LastCheckedAt *metav1.Time `json:"lastCheckedAt,omitempty"`
	// Attempts counts checks that ended without a verdict (registry or SCM
	// unreachable, signature not there yet); it spaces the retries.
	// +optional
	Attempts int `json:"attempts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=iv,categories=kardinal-all
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipelineName`
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.spec.bundleName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ImageVerification checks the signatures of one Bundle's images (and of
// its config commit) before the Bundle is promoted. Created by the Bundle's
// kro Graph; reconciled by the ImageVerification reconciler.
type ImageVerification struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImageVerificationSpec   `json:"spec,omitempty"`
	Status ImageVerificationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ImageVerificationList contains a list of ImageVerification.
type ImageVerificationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageVerification `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImageVerification{}, &ImageVerificationList{})
}
