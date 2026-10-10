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

	// Timeout bounds how long a missing signature is waited for (CI may sign
	// after it pushes), from the moment the ImageVerification starts. A
	// signature that does not verify fails at once. Empty or "0" means 10m.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`
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
	// SignatureNotVerified, CommitNotVerified, Timeout.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Images has one result per spec.images entry.
	// +optional
	Images []ImageVerificationResult `json:"images,omitempty"`
	// Commit is the commit result.
	// +optional
	Commit *CommitVerificationResult `json:"commit,omitempty"`
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
// +kubebuilder:resource:scope=Namespaced,shortName=iv
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
