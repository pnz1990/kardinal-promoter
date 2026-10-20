// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MaxReceiptStatementBytes bounds a PromotionReceipt's statement (256 KiB):
// the statement builder cuts the longest lists past it (docs/receipts.md).
const MaxReceiptStatementBytes = 256 * 1024

// Receipt phases.
const (
	// ReceiptPending: the statement is not signed yet.
	ReceiptPending = "Pending"
	// ReceiptSigned: status.bundle holds the signed statement; a configured
	// sink outside the cluster is not written yet.
	ReceiptSigned = "Signed"
	// ReceiptStored: signed, and every configured sink holds it.
	ReceiptStored = "Stored"
	// ReceiptFailed: the receipt can never be signed: its statement does not
	// match spec.statementDigest, or is not an in-toto statement.
	ReceiptFailed = "Failed"
)

// Receipt sinks (status.stored[].sink).
const (
	ReceiptSinkCRD   = "crd"
	ReceiptSinkOCI   = "oci"
	ReceiptSinkRekor = "rekor"
)

// Receipt signing modes (status.signedBy.mode, --receipt-signing-mode).
const (
	ReceiptSigningKeyless = "keyless"
	ReceiptSigningKey     = "key"
	ReceiptSigningKMS     = "kms"
)

// PromotionReceiptSpec is the record of one promotion that reached
// Verified. The PromotionStep reconciler creates it once, at the Verified
// transition; it never changes.
type PromotionReceiptSpec struct {
	// Statement is the in-toto statement (JSON): a SLSA verification summary
	// (https://slsa.dev/verification_summary/v1) whose kardinalPromotion
	// extension holds the record: the version, the actor, the gates as they
	// were when the step started, the approvals, the checks and the change.
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:MaxLength=262144
	Statement string `json:"statement"`

	// StatementDigest is sha256:<hex> of Statement. A receipt whose
	// statement does not match it is never signed (phase Failed).
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	StatementDigest string `json:"statementDigest"`

	// PullRequest is the promotion's PR, which the receipt reconciler
	// comments on once the receipt is signed. Empty for a promotion without
	// a PR (approval: auto).
	// +optional
	PullRequest *ReceiptPullRequest `json:"pullRequest,omitempty"`
}

// ReceiptPullRequest names a promotion PR.
type ReceiptPullRequest struct {
	// Repo is the repository, owner/name.
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`
	// Number is the PR number.
	// +kubebuilder:validation:Minimum=1
	Number int `json:"number"`
	// URL is the PR's web address.
	// +optional
	URL string `json:"url,omitempty"`
	// ScmProvider is the ScmProvider the PR was opened on; nil is the
	// controller's own SCM.
	// +optional
	ScmProvider *ScmProviderIdentity `json:"scmProvider,omitempty"`
}

// ReceiptSigner says who signed a receipt.
type ReceiptSigner struct {
	// Mode is keyless, key or kms.
	Mode string `json:"mode"`
	// Identity is the certificate's subject alternative name (keyless), or
	// the key reference (kms) or the Secret (key).
	// +optional
	Identity string `json:"identity,omitempty"`
	// Issuer is the OIDC issuer the certificate names (keyless).
	// +optional
	Issuer string `json:"issuer,omitempty"`
	// KeyHint is the base64 sha256 of the public key (key and kms).
	// +optional
	KeyHint string `json:"keyHint,omitempty"`
}

// ReceiptStorage records one sink that holds the signed receipt.
type ReceiptStorage struct {
	// Sink is crd, oci or rekor.
	Sink string `json:"sink"`
	// Ref is the OCI referrer (repository@sha256:...), for sink oci.
	// +optional
	Ref string `json:"ref,omitempty"`
	// Subject is the image the OCI referrer refers to, for sink oci.
	// +optional
	Subject string `json:"subject,omitempty"`
	// LogIndex is the transparency log index, for sink rekor.
	// +optional
	LogIndex *int64 `json:"logIndex,omitempty"`
	// URL is the transparency log, for sink rekor.
	// +optional
	URL string `json:"url,omitempty"`
	// At is when the sink was written.
	At metav1.Time `json:"at"`
}

// PromotionReceiptStatus is written by the receipt reconciler only.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.bundle) || (has(self.bundle) && self.bundle == oldSelf.bundle)",message="status.bundle cannot change once set"
type PromotionReceiptStatus struct {
	// Phase is Pending, Signed, Stored or Failed.
	// +kubebuilder:validation:Enum=Pending;Signed;Stored;Failed
	// +optional
	Phase string `json:"phase,omitempty"`

	// Bundle is the Sigstore bundle (JSON, media type
	// application/vnd.dev.sigstore.bundle.v0.3+json) of the signed
	// statement: a DSSE envelope with the certificate or key hint, and the
	// transparency log entry or timestamp when there is one. Set once.
	// +kubebuilder:validation:MaxLength=786432
	// +optional
	Bundle string `json:"bundle,omitempty"`

	// Bytes is the size of spec.statement and status.bundle, written with
	// the signature: they leave the controller's cache once the receipt is
	// done, and the Pipeline's status.environments[].receipts sums it.
	// +optional
	Bytes int64 `json:"bytes,omitempty"`

	// SignedBy says who signed the receipt.
	// +optional
	SignedBy *ReceiptSigner `json:"signedBy,omitempty"`

	// Stored lists the sinks holding the signed receipt: the CR, the
	// transparency log, and an OCI referrer per image. A Bundle has at most
	// 100 images, so at most 102 (#2331).
	// +kubebuilder:validation:MaxItems=102
	// +optional
	Stored []ReceiptStorage `json:"stored,omitempty"`

	// Attempts counts the failed sign or store attempts since the last
	// progress.
	// +optional
	Attempts int `json:"attempts,omitempty"`

	// NextRetryAt is when a failed sign or sink is tried again: after the
	// backoff, or after the Retry-After that Fulcio, Rekor or the timestamp
	// authority answered (at most 5 minutes), with jitter.
	// +optional
	NextRetryAt *metav1.Time `json:"nextRetryAt,omitempty"`

	// LastAttemptAt is when the last failed sign or store attempt ran.
	// +optional
	LastAttemptAt *metav1.Time `json:"lastAttemptAt,omitempty"`

	// PRComment is true once the receipt reconciler commented on the
	// promotion's PR, so a retry does not comment twice.
	// +optional
	PRComment bool `json:"prComment,omitempty"`

	// Conditions are Signed and Stored, with reasons naming a failing sink.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=receipt;receipts,categories=kardinal-all
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.metadata.labels.kardinal\.io/pipeline`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.metadata.labels.kardinal\.io/environment`
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.metadata.labels.kardinal\.io/bundle`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionReceipt is the signed record of one promotion (docs/receipts.md).
// It has no owner: it outlives the Bundle, the Graph and the step.
type PromotionReceipt struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a PromotionReceipt's spec is immutable"
	Spec   PromotionReceiptSpec   `json:"spec"`
	Status PromotionReceiptStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromotionReceiptList contains a list of PromotionReceipt.
type PromotionReceiptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionReceipt `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromotionReceipt{}, &PromotionReceiptList{})
}

// MaxDecisionGates bounds status.decision.gates; the gates past it are
// counted in status.decision.omittedGates.
const MaxDecisionGates = 64

// MaxDecisionBytes bounds a step's status.decision as JSON: it is written in
// the patch that moves the step to Promoting, next to the rest of the
// status, and must stay well under the API server's request limit.
const MaxDecisionBytes = 128 * 1024

// PromotionDecision is what a PromotionStep started on: its required gates
// as it read them when it left Pending, and the policy versions. The
// receipt records it, so a gate that changes later does not change the
// receipt.
type PromotionDecision struct {
	// TakenAt is when the step left Pending.
	TakenAt metav1.Time `json:"takenAt"`

	// Gates are the required gates, in spec.requiredGates order.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Gates []DecisionGate `json:"gates,omitempty"`

	// OmittedGates counts the gates past MaxDecisionGates, or past
	// MaxDecisionBytes.
	// +optional
	OmittedGates int `json:"omittedGates,omitempty"`

	// OmittedGateNames name the gates left out by MaxDecisionGates or
	// MaxDecisionBytes, at most MaxDecisionGates of them (OmittedGates counts
	// them all).
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +optional
	OmittedGateNames []string `json:"omittedGateNames,omitempty"`

	// Facts are what every required gate showed, decided before any cut:
	// the receipt's levels come from them, so a gate left out of the
	// snapshot still counts (an override on it is still OVERRIDDEN).
	// +optional
	Facts *DecisionFacts `json:"facts,omitempty"`

	// ChangeRequests are the step's ChangeRequest instances (the tickets
	// of its Bundle and environment, docs/change-requests.md) as the step
	// read them when it left Pending: the inputs its gates read as
	// changeRequests.<name>.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	ChangeRequests []DecisionChangeRequest `json:"changeRequests,omitempty"`

	// Truncated is true when the snapshot was cut to MaxDecisionBytes:
	// approvals and Verdicts that did not count, long gate texts, change
	// request approvers, or gates (OmittedGates). PolicyDigest covers every
	// gate.
	// +optional
	Truncated bool `json:"truncated,omitempty"`

	// PipelineGeneration is the Pipeline's metadata.generation.
	// +optional
	PipelineGeneration int64 `json:"pipelineGeneration,omitempty"`

	// PipelineSpecHash is the Bundle's status.pipelineSpecHash.
	// +optional
	PipelineSpecHash string `json:"pipelineSpecHash,omitempty"`

	// PolicyDigest is sha256:<hex> of the canonical policy set: the
	// Pipeline generation and spec hash, and each gate template's namespace,
	// name, UID, generation and spec digest.
	// +optional
	PolicyDigest string `json:"policyDigest,omitempty"`
}

// DecisionFacts are the level facts of a step's required gates, over all
// of them.
type DecisionFacts struct {
	// Gates is how many required gates the step read.
	Gates int `json:"gates"`
	// Passed: every one was ready. Approved: one counted an approval.
	// Overridden: one passed by an override. VerifiedByAgent: a verify gate
	// counted a passing Verdict.
	Passed          bool `json:"passed"`
	Approved        bool `json:"approved"`
	Overridden      bool `json:"overridden"`
	VerifiedByAgent bool `json:"verifiedByAgent"`
}

// DecisionGate is one required gate as the step read it.
type DecisionGate struct {
	// Name is the gate instance.
	Name string `json:"name"`
	// Template is the gate template, namespace/name.
	// +optional
	Template string `json:"template,omitempty"`
	// TemplateUID is the template's UID.
	// +optional
	TemplateUID string `json:"templateUID,omitempty"`
	// TemplateGeneration is the template's metadata.generation.
	// +optional
	TemplateGeneration int64 `json:"templateGeneration,omitempty"`
	// TemplateDigest is sha256:<hex> of the template's canonical spec.
	// +optional
	TemplateDigest string `json:"templateDigest,omitempty"`
	// Expression is the gate's CEL expression.
	// +optional
	Expression string `json:"expression,omitempty"`
	// Message is the gate's message.
	// +optional
	Message string `json:"message,omitempty"`
	// Ready is the gate's status.ready.
	Ready bool `json:"ready"`
	// Reason is the gate's status.reason.
	// +optional
	Reason string `json:"reason,omitempty"`
	// EvaluatedAt is the gate's status.lastEvaluatedAt.
	// +optional
	EvaluatedAt *metav1.Time `json:"evaluatedAt,omitempty"`
	// Override is the override the gate passed by, when it did.
	// +optional
	Override *DecisionOverride `json:"override,omitempty"`
	// ApprovalRequired is true for a gate with spec.approval.
	// +optional
	ApprovalRequired bool `json:"approvalRequired,omitempty"`
	// Approvals are the gate's status.approvals.
	// +kubebuilder:validation:MaxItems=101
	// +optional
	Approvals []GateApprovalStatus `json:"approvals,omitempty"`
	// VerifyRequired is true for a verify gate (spec.verify).
	// +optional
	VerifyRequired bool `json:"verifyRequired,omitempty"`
	// Verdicts are the gate's status.verdicts.
	// +kubebuilder:validation:MaxItems=101
	// +optional
	Verdicts []GateVerdictStatus `json:"verdicts,omitempty"`

	// Truncated is true when the snapshot cut this gate: approvals or
	// Verdicts that did not count, or long texts.
	// +optional
	Truncated bool `json:"truncated,omitempty"`
}

// DecisionOverride is the override a gate passed by.
type DecisionOverride struct {
	// CreatedBy is who created it.
	// +optional
	CreatedBy string `json:"createdBy,omitempty"`
	// Reason is its reason.
	// +optional
	Reason string `json:"reason,omitempty"`
	// ExpiresAt is its expiresAt.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// Verified is true when the identity admission policy checked createdBy.
	// +optional
	Verified bool `json:"verified,omitempty"`
	// Actor is its actor stamp.
	// +optional
	Actor *Actor `json:"actor,omitempty"`
}

// MaxDecisionChangeRequests bounds status.decision.changeRequests.
const MaxDecisionChangeRequests = 32

// DecisionChangeRequest is one ChangeRequest instance as a step read it.
type DecisionChangeRequest struct {
	// Name is the ChangeRequest template the instance was made from: the
	// name a gate reads as changeRequests.<name>.
	Name string `json:"name"`
	// Instance is the ChangeRequest instance.
	Instance string `json:"instance"`
	// Provider is servicenow or jira.
	// +optional
	Provider string `json:"provider,omitempty"`
	// Ticket is the ticket the instance tracks; nil when it has none yet.
	// +optional
	Ticket *ChangeRequestTicket `json:"ticket,omitempty"`
	// State is the instance's status.state.
	// +optional
	State string `json:"state,omitempty"`
	// ProviderState is the instance's status.providerState.
	// +optional
	ProviderState string `json:"providerState,omitempty"`
	// Verified reports whether the ticket is this promotion's: one kardinal
	// opened, or a linked one the reconciler verified
	// (ChangeRequestStatus.TicketVerified).
	Verified bool `json:"verified"`
	// VerifiedReason is the instance's status.verifiedReason.
	// +optional
	VerifiedReason string `json:"verifiedReason,omitempty"`
	// Approvers are the ticket's approvers, as the provider reported them.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Approvers []ChangeRequestApprover `json:"approvers,omitempty"`
	// ApproversOmitted counts the approvers the snapshot cut.
	// +optional
	ApproversOmitted int `json:"approversOmitted,omitempty"`
	// TimedOut is the instance's status.timedOut.
	// +optional
	TimedOut bool `json:"timedOut,omitempty"`
	// ObservedAt is when the ticket was last read.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	// ValidUntil is until when that reading counts: a gate evaluated later
	// reads the ticket's state as unknown.
	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`
}

// PendingReceipt is the receipt outbox of a PromotionStep: the receipt it
// decided to create at Verified, stored in the same status patch, until the
// PromotionReceipt exists.
type PendingReceipt struct {
	// Name is the PromotionReceipt name.
	Name string `json:"name"`
	// Spec is the PromotionReceipt spec.
	Spec PromotionReceiptSpec `json:"spec"`
}
