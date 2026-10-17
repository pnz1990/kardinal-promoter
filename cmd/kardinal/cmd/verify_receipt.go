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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	sigtuf "github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
)

// verifyReceiptOptions are the flags of kardinal verify-receipt.
type verifyReceiptOptions struct {
	bundle, image, env, file, repo string
	pipeline, subject, cluster     string
	key, identity, issuer, trRoot  string
	require                        []string
	insecure, anyCluster           bool
}

func newVerifyReceiptCmd() *cobra.Command {
	var o verifyReceiptOptions
	cmd := &cobra.Command{
		Use:   "verify-receipt [<pipeline> <env>]",
		Short: "Verify a signed promotion receipt: its signature, subject, environment and levels",
		Long: `Verify a promotion's signed receipt (docs/receipts.md), from the cluster
(<pipeline> <env>, the newest unless --bundle), an image's OCI referrers
(--image) or a file (--file, with -n, --pipeline, --env and --subject). Check
it with --key, or with --identity and --issuer (both required: no default
trusts any identity). The exit code is 0 only when every check passes.`,
		Annotations: map[string]string{ReferenceAnnotation: verifyReceiptReference},
		Example: `  kardinal verify-receipt checkout prod --key receipt.pub
  kardinal verify-receipt checkout prod --bundle checkout-1-5-0 \
    --identity https://kubernetes.io/namespaces/kardinal-system/serviceaccounts/kardinal-promoter \
    --issuer https://oidc.eks.us-east-2.amazonaws.com/id/EXAMPLE
  kardinal verify-receipt --image ghcr.io/acme/checkout@sha256:9f2c... -n payments --pipeline checkout --env prod --cluster 1f0c... --key receipt.pub
  kardinal verify-receipt --file receipt.sigstore.json -n payments --pipeline checkout --env prod \
    --cluster 1f0c... --subject sha256:9f2c... --key receipt.pub --require GATES_PASSED,APPROVED,!OVERRIDDEN`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerifyReceipt(cmd, args, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.bundle, "bundle", "", "The Bundle whose receipt to verify (default: the newest receipt of the environment)")
	f.StringVar(&o.image, "image", "", "Read the receipt from this image's OCI referrers (repository@sha256:...)")
	f.StringVar(&o.env, "env", "", "The environment the receipt must be for (with --image or --file)")
	f.StringVar(&o.file, "file", "", "Read the receipt from this Sigstore bundle file")
	f.StringVar(&o.pipeline, "pipeline", "", "The Pipeline the receipt must be for (required with --image and --file, with -n)")
	f.StringVar(&o.subject, "subject", "", "The image digest (sha256:...) or commit the receipt must be about (required with --file)")
	f.StringVar(&o.cluster, "cluster", "", "The cluster identity the receipt must name (its kube-system Namespace UID, or receipts.clusterID); required with --image and --file")
	f.BoolVar(&o.anyCluster, "any-cluster", false, "With --image or --file, accept a receipt of any cluster (prints a warning)")
	f.StringVar(&o.key, "key", "", "Public key file (PEM) of a key or KMS receipt")
	f.StringVar(&o.identity, "identity", "", "Certificate identity (subject alternative name) of a keyless receipt")
	f.StringVar(&o.issuer, "issuer", "", "OIDC issuer of a keyless receipt")
	f.StringVar(&o.trRoot, "trusted-root", "", "Sigstore trusted_root.json (default: the public-good root, fetched with TUF)")
	f.StringSliceVar(&o.require, "require", nil, "Levels that must be verified, comma-separated (GATES_PASSED, APPROVED, HEALTHY, ...); !LEVEL must be absent")
	f.StringVar(&o.repo, "repository", "", "Read --image's receipts from this repository (receipts.sinks.oci.repository) instead of the image's")
	f.BoolVar(&o.insecure, "insecure-registry", false, "Read --image over plain HTTP")
	return withStructuredOutput(cmd)
}

// verifyReceiptResult is -o json|yaml of verify-receipt.
type verifyReceiptResult struct {
	Receipt  string `json:"receipt"`
	Verified bool   `json:"verified"`
	Signer   string `json:"signer"`
	Logged   bool   `json:"logged"`
	// LogNotChecked: the receipt has a log entry that was not verified.
	LogNotChecked bool               `json:"logNotChecked,omitempty"`
	Statement     *receipt.Statement `json:"statement"`
}

func runVerifyReceipt(cmd *cobra.Command, args []string, o verifyReceiptOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	sources := 0
	for _, set := range []bool{len(args) > 0, o.image != "", o.file != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return errors.New("verify-receipt: give <pipeline> <env>, --image or --file (one of them)")
	}
	if len(args) == 1 {
		return errors.New("verify-receipt: give both <pipeline> and <env>")
	}
	vo := receipt.VerifyOptions{Identity: o.identity, Issuer: o.issuer, Environment: o.env, Require: o.require,
		Pipeline: o.pipeline, ClusterID: o.cluster, Subject: o.subject}
	if len(args) == 0 {
		// Outside the cluster nothing else ties the receipt to a tenant.
		if globalNamespace == "" || o.pipeline == "" || o.env == "" {
			return errors.New("verify-receipt: --image and --file need -n, --pipeline and --env: a receipt is verified for " +
				"one namespace, Pipeline and environment, or another tenant's receipt of the same image would pass")
		}
		vo.Namespace = globalNamespace
		vo.RequireCluster = !o.anyCluster
		if o.anyCluster && o.cluster == "" {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: --any-cluster: a receipt of any cluster, signed with this key or identity, passes")
		}
		if o.file != "" && o.subject == "" {
			return errors.New("verify-receipt: --file needs --subject (the image digest sha256:... or the commit it must be about)")
		}
	}
	if o.key != "" {
		raw, err := os.ReadFile(o.key)
		if err != nil {
			return fmt.Errorf("verify-receipt: --key: %w", err)
		}
		vo.Key = raw
	}
	if o.trRoot != "" || o.identity != "" {
		tr, err := trustedRoot(o.trRoot)
		if err != nil {
			return fmt.Errorf("verify-receipt: %w", err)
		}
		vo.TrustedRoot = tr
	}

	var bundles [][]byte
	var name string
	switch {
	case len(args) == 2:
		vo.Environment, vo.Pipeline = args[1], args[0]
		rc, err := clusterReceipt(ctx, args[0], args[1], o.bundle)
		if err != nil {
			return fmt.Errorf("verify-receipt: %w", err)
		}
		vo.Namespace = rc.Namespace
		vo.Statement = []byte(rc.Spec.Statement)
		bundles, name = [][]byte{[]byte(rc.Status.Bundle)}, rc.Name
	case o.file != "":
		raw, err := os.ReadFile(o.file)
		if err != nil {
			return fmt.Errorf("verify-receipt: --file: %w", err)
		}
		bundles, name = [][]byte{raw}, o.file
	default:
		repo, digest, ok := strings.Cut(o.image, "@")
		if !ok || !strings.HasPrefix(digest, "sha256:") {
			return fmt.Errorf("verify-receipt: --image %q needs a digest (repository@sha256:...)", o.image)
		}
		if o.subject != "" && o.subject != digest {
			return fmt.Errorf("verify-receipt: --subject %s is not --image's digest %s", o.subject, digest)
		}
		vo.Subject = digest
		reg := &receipt.Registry{Keychain: authn.DefaultKeychain, Insecure: o.insecure}
		if o.repo != "" {
			repo = o.repo
		}
		found, err := reg.Referrers(ctx, repo, digest)
		if err != nil {
			return fmt.Errorf("verify-receipt: %w", err)
		}
		if len(found) == 0 {
			return fmt.Errorf("verify-receipt: %s has no receipt referrers", o.image)
		}
		bundles, name = found, o.image
	}

	var errs []error
	for _, b := range bundles {
		v, err := receipt.Verify(b, vo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(args) == 2 {
			if err := checkBundleUID(ctx, v.Statement); err != nil {
				return fmt.Errorf("verify-receipt: %s: %w", name, err)
			}
		}
		if v.Statement.Predicate.Kardinal.Receipt != "" {
			name = v.Statement.Predicate.Kardinal.Receipt
		}
		out := cmd.OutOrStdout()
		if structured() {
			return writeStructured(out, verifyReceiptResult{Receipt: name, Verified: true, Signer: v.Signer, Logged: v.Logged,
				LogNotChecked: v.LogNotChecked, Statement: v.Statement})
		}
		printReceipt(out, name, v)
		return nil
	}
	return fmt.Errorf("verify-receipt: %s: NOT VERIFIED: %w", name, errors.Join(errs...))
}

// clusterReceipt is the receipt of pipeline's env: of bundle, else the
// newest. A receipt not signed yet is an error.
func clusterReceipt(ctx context.Context, pipeline, env, bundle string) (*v1alpha1.PromotionReceipt, error) {
	c, ns, err := buildClient()
	if err != nil {
		return nil, err
	}
	labels := sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline, "kardinal.io/environment": env}
	if bundle != "" {
		labels["kardinal.io/bundle"] = bundle
	}
	var list v1alpha1.PromotionReceiptList
	if err := c.List(ctx, &list, sigs_client.InNamespace(ns), labels); err != nil {
		return nil, fmt.Errorf("list promotion receipts: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no receipt for pipeline %s, environment %s%s in namespace %s", pipeline, env, forBundle(bundle), ns)
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[j].CreationTimestamp.Before(&list.Items[i].CreationTimestamp)
	})
	rc := &list.Items[0]
	if rc.Status.Bundle == "" {
		return nil, fmt.Errorf("receipt %s is not signed (phase %s): kubectl describe promotionreceipt %s", rc.Name, phaseOr(rc.Status.Phase), rc.Name)
	}
	if receipt.Digest([]byte(rc.Spec.Statement)) != rc.Spec.StatementDigest {
		return nil, fmt.Errorf("receipt %s: spec.statement does not match spec.statementDigest: the receipt was changed", rc.Name)
	}
	return rc, nil
}

// checkBundleUID checks that the Bundle the receipt names, when it still
// exists, is the one it was made for.
func checkBundleUID(ctx context.Context, st *receipt.Statement) error {
	c, ns, err := buildClient()
	if err != nil {
		return err
	}
	v := st.Predicate.Kardinal.Version
	var b v1alpha1.Bundle
	err = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: v.Bundle}, &b)
	switch {
	case apierrors.IsNotFound(err):
		return nil // retired: the receipt outlives it
	case err != nil:
		return fmt.Errorf("get bundle %s: %w", v.Bundle, err)
	case string(b.UID) != v.BundleUID:
		return fmt.Errorf("the receipt is for Bundle %s with UID %s, and the Bundle of that name now has UID %s", v.Bundle, v.BundleUID, b.UID)
	}
	return nil
}

func forBundle(b string) string {
	if b == "" {
		return ""
	}
	return ", bundle " + b
}

func phaseOr(p string) string {
	if p == "" {
		return v1alpha1.ReceiptPending
	}
	return p
}

// trustedRoot reads path, or fetches the public-good root with TUF.
func trustedRoot(path string) (sigroot.TrustedMaterial, error) {
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("--trusted-root: %w", err)
		}
		tr, err := sigroot.NewTrustedRootFromJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("--trusted-root: %w", err)
		}
		return tr, nil
	}
	opts := sigtuf.DefaultOptions().WithDisableLocalCache()
	tr, err := sigroot.FetchTrustedRootWithOptions(opts)
	if err != nil {
		return nil, fmt.Errorf("fetch the public-good trusted root (or give --trusted-root): %w", err)
	}
	return tr, nil
}

// printReceipt is the human output: what was promoted, by whom, through
// which gates, and who signed it.
func printReceipt(w io.Writer, name string, v *receipt.Verified) {
	p := v.Statement.Predicate
	r := p.Kardinal
	_, _ = fmt.Fprintf(w, "VERIFIED receipt %s\n", name)
	_, _ = fmt.Fprintf(w, "  environment: %s (pipeline %s)\n", r.Change.Environment, r.Pipeline)
	ver := r.Version.Bundle
	if r.Version.Version != "" {
		ver += " (" + r.Version.Version + ")"
	}
	if r.RollbackOf != "" {
		ver += ", a rollback of " + r.RollbackOf
	}
	_, _ = fmt.Fprintf(w, "  version:     %s\n", ver)
	if r.Actor != nil {
		who := actor.Describe(*r.Actor)
		if a := actor.DescribeAsserted(*r.Actor); a != "" {
			who += "; " + a
		}
		_, _ = fmt.Fprintf(w, "  actor:       %s\n", who)
	}
	signed := v.Signer
	if v.LogNotChecked {
		signed += "; its transparency log entry is not checked (give --trusted-root)"
	}
	if v.Logged {
		signed += ", in the transparency log"
	}
	_, _ = fmt.Fprintf(w, "  signed by:   %s\n", signed)
	if pr := r.Change.PR; pr != nil && pr.URL != "" {
		_, _ = fmt.Fprintf(w, "  change:      %s\n", pr.URL)
	} else if r.Change.Commit != "" {
		_, _ = fmt.Fprintf(w, "  change:      commit %s\n", r.Change.Commit)
	}
	var approvers, verifiers []string
	for _, g := range r.Gates {
		line := fmt.Sprintf("  gate %s: %s", g.Name, g.Result)
		if g.Reason != "" {
			line += " (" + g.Reason + ")"
		}
		if g.Override != nil {
			line += fmt.Sprintf(" OVERRIDDEN by %s: %s", g.Override.CreatedBy, g.Override.Reason)
		}
		_, _ = fmt.Fprintln(w, line)
		for _, a := range g.Approvals {
			if a.Counted && a.Decision == "approve" {
				approvers = append(approvers, a.User)
			}
		}
		for _, v := range g.Verdicts {
			if v.Counted && v.Result == "pass" {
				verifiers = append(verifiers, v.Verifier)
			}
		}
	}
	if r.OmittedGates > 0 {
		_, _ = fmt.Fprintf(w, "  (%d more gates not in the receipt)\n", r.OmittedGates)
	}
	if !r.DecisionSnapshot {
		_, _ = fmt.Fprintln(w, "  gates read when the step was verified: it started before receipts were enabled")
	}
	if len(approvers) > 0 {
		_, _ = fmt.Fprintf(w, "  approved by: %s\n", strings.Join(approvers, ", "))
	}
	if len(verifiers) > 0 {
		_, _ = fmt.Fprintf(w, "  verified by: %s\n", strings.Join(verifiers, ", "))
	}
	_, _ = fmt.Fprintf(w, "  levels:      %s\n", strings.Join(p.VerifiedLevels, ", "))
	if d := p.Policy.Digest["sha256"]; d != "" {
		_, _ = fmt.Fprintf(w, "  policy:      sha256:%s\n", d)
	}
	if r.Truncated {
		_, _ = fmt.Fprintln(w, "  (truncated: the record was cut to the receipt size cap)")
	}
}

// verifyReceiptReference is the detail of the command for its reference
// page.
const verifyReceiptReference = `A receipt names the cluster, namespace, Pipeline and environment it is
for, and its subjects. From the cluster they are the receipt's own; with
--image and --file pin them (-n, --pipeline and --env, --subject for a file,
and --cluster when the receipt names a cluster, or --any-cluster), so another
tenant's or another cluster's receipt of the same image is refused.

It checks the signature (and the certificate chain against the trusted
root); the transparency log entry or timestamp when there is one; the
subject; that the summary is PASSED for the environment asked; and each
--require'd level (a leading ! means it must be absent). From the cluster, the receipt's spec.statement must be the signed statement
byte for byte, and the Bundle it names, when it still exists, must have the
UID the receipt records: a PromotionReceipt edited after it was signed fails.
The command never contacts Rekor: it checks the log entry and inclusion proof
embedded in the bundle against the trusted root. --image reads the registry
with your docker credentials (~/.docker/config.json). A KMS receipt is
verified with the KMS key's public key (--key, PEM).`
