// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"encoding/pem"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
)

const (
	gitsignIdentity = "https://github.com/myorg/agent/.github/workflows/fix.yml@refs/heads/main"
	gitsignIssuer   = "https://token.actions.githubusercontent.com"
)

// testPayload is a commit object without its signature header.
const testPayload = "tree 3cced1b431769cf16b530b25162e23bb7545d79c\n" +
	"parent 4638d83b00e1119b9aab39ff747802a8ff63f530\n" +
	"author Fixer <fixer@example.com> 1777409421 -0400\n" +
	"committer Fixer <fixer@example.com> 1777414757 -0400\n" +
	"\n" +
	"fix: bump the image\n"

// signOpts are signtest.GitsignOptions.
type signOpts = signtest.GitsignOptions

// signCommit is a gitsign-style signature of payload by a leaf certificate
// of vs for identity and issuer (signtest.GitsignSign).
func signCommit(t *testing.T, vs *ca.VirtualSigstore, identity, issuer string, payload []byte, o signOpts) []byte {
	t.Helper()
	sig, _ := signCommitEntry(t, vs, identity, issuer, payload, o)
	return sig
}

// signCommitEntry is signCommit, and the log entry it embedded.
func signCommitEntry(t *testing.T, vs *ca.VirtualSigstore, identity, issuer string, payload []byte, o signOpts) ([]byte, *rekorpb.TransparencyLogEntry) {
	t.Helper()
	sig, tle, err := signtest.GitsignSign(vs, identity, issuer, payload, o)
	require.NoError(t, err)
	return sig, tle
}

func keylessAuthority(t *testing.T, vs *ca.VirtualSigstore, name, issuer, subject string) authority {
	t.Helper()
	id, err := verify.NewShortCertificateIdentity(issuer, "", subject, "")
	require.NoError(t, err)
	return authority{name: name, trusted: vs, identity: &id}
}

func TestGitsign_VerifiesAgainstAKeylessAuthority(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	agent := keylessAuthority(t, vs, "agents", gitsignIssuer, gitsignIdentity)
	other := keylessAuthority(t, vs, "release", gitsignIssuer, "https://github.com/myorg/app/.github/workflows/release.yml@refs/heads/main")
	ctx := context.Background()

	for name, o := range map[string]signOpts{"signed entry timestamp": {}, "inclusion proof": {Proof: true}} {
		t.Run(name, func(t *testing.T) {
			sig := signCommit(t, vs, gitsignIdentity, gitsignIssuer, []byte(testPayload), o)
			c, err := parseGitsign(ctx, []byte(testPayload), sig)
			require.NoError(t, err)
			res, signer, err := agent.verifyBundle(c.bundle, c.digest)
			require.NoError(t, err)
			assert.Equal(t, gitsignIdentity, signer)
			assert.Equal(t, gitsignIssuer, res.Signature.Certificate.Issuer)

			_, _, err = other.verifyBundle(c.bundle, c.digest)
			assert.Error(t, err, "another identity's authority refuses it")
		})
	}
}

func TestGitsign_Refusals(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	auth := keylessAuthority(t, vs, "agents", gitsignIssuer, gitsignIdentity)
	ctx := context.Background()
	payload := []byte(testPayload)
	verifyErr := func(t *testing.T, payload, sig []byte) error {
		t.Helper()
		c, err := parseGitsign(ctx, payload, sig)
		if err != nil {
			return err
		}
		_, _, err = auth.verifyBundle(c.bundle, c.digest)
		return err
	}

	t.Run("a payload the signature is not over", func(t *testing.T) {
		sig := signCommit(t, vs, gitsignIdentity, gitsignIssuer, payload, signOpts{})
		err := verifyErr(t, []byte(strings.Replace(testPayload, "bump", "drop", 1)), sig)
		assert.ErrorContains(t, err, "messageDigest mismatch")
	})
	t.Run("no embedded Rekor entry", func(t *testing.T) {
		sig := signCommit(t, vs, gitsignIdentity, gitsignIssuer, payload, signOpts{NoEntry: true})
		assert.ErrorContains(t, verifyErr(t, payload, sig), "no embedded Rekor entry")
	})
	t.Run("an entry logged for another signature", func(t *testing.T) {
		// A real entry of the same identity, for another commit.
		_, entry := signCommitEntry(t, vs, gitsignIdentity, gitsignIssuer, []byte(strings.Replace(testPayload, "bump", "pin", 1)), signOpts{Proof: true})
		promiseOnly := proto.Clone(entry).(*rekorpb.TransparencyLogEntry)
		promiseOnly.InclusionProof = nil
		for name, e := range map[string]*rekorpb.TransparencyLogEntry{"inclusion proof": entry, "signed entry timestamp": promiseOnly} {
			sig := signCommit(t, vs, gitsignIdentity, gitsignIssuer, payload, signOpts{Entry: e})
			assert.Error(t, verifyErr(t, payload, sig), name)
		}
	})
	t.Run("a certificate expired at the Rekor time", func(t *testing.T) {
		sig := signCommit(t, vs, gitsignIdentity, gitsignIssuer, payload, signOpts{At: time.Now().Add(2 * time.Hour)})
		assert.Error(t, verifyErr(t, payload, sig))
	})
	t.Run("a certificate from another Sigstore", func(t *testing.T) {
		other, err := ca.NewVirtualSigstore()
		require.NoError(t, err)
		sig := signCommit(t, other, gitsignIdentity, gitsignIssuer, payload, signOpts{Proof: true})
		assert.Error(t, verifyErr(t, payload, sig))
	})
	t.Run("GPG", func(t *testing.T) {
		gpg := []byte("-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCAAdFiEE\n-----END PGP SIGNATURE-----\n")
		err := verifyErr(t, payload, gpg)
		assert.ErrorIs(t, err, errNotGitsign)
		assert.ErrorContains(t, err, "GPG")
	})
	t.Run("SSH", func(t *testing.T) {
		ssh := []byte("-----BEGIN SSH SIGNATURE-----\nU1NIU0lH\n-----END SSH SIGNATURE-----\n")
		assert.ErrorIs(t, verifyErr(t, payload, ssh), errNotGitsign)
	})
	t.Run("unsigned", func(t *testing.T) {
		assert.ErrorIs(t, verifyErr(t, payload, nil), errNotGitsign)
	})
	t.Run("not CMS", func(t *testing.T) {
		junk := pem.EncodeToMemory(&pem.Block{Type: gitsignPEMType, Bytes: []byte("junk")})
		assert.ErrorContains(t, verifyErr(t, payload, junk), "parse the CMS signature")
	})
}

func TestCommitObjectID(t *testing.T) {
	sig := "-----BEGIN SIGNED MESSAGE-----\nMIIEMQYJKoZIhvcNAQcCoIIEIjCCBB4CAQExDTALBglghkgBZQMEAgEw\nCwYJKoZIhvcNAQcB\n-----END SIGNED MESSAGE-----\n"
	// The commit as git stores it: the signature is the last header.
	raw := strings.Replace(testPayload, "\n\n",
		"\ngpgsig "+strings.ReplaceAll(strings.TrimRight(sig, "\n"), "\n", "\n ")+"\n\n", 1)
	got, err := commitObjectID([]byte(testPayload), []byte(sig), false)
	require.NoError(t, err)
	want := gitHashObject(t, raw)
	assert.Equal(t, want, got)

	sha256ID, err := commitObjectID([]byte(testPayload), []byte(sig), true)
	require.NoError(t, err)
	assert.Len(t, sha256ID, 64)
	assert.NotEqual(t, got, sha256ID[:40])

	_, err = commitObjectID([]byte("tree abc\n"), []byte(sig), false)
	assert.ErrorContains(t, err, "no blank line")
}

// gitHashObject is git's object ID of commit object raw, or the same
// computation when git is not installed.
func gitHashObject(t *testing.T, raw string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cmd := exec.Command("git", "hash-object", "-t", "commit", "--literally", "--stdin")
	cmd.Stdin = strings.NewReader(raw)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// TestGitsign_GoGitCommitAndTrustedRoot: a commit go-git signs through
// signtest.GitsignSigner has the object ID commitObjectID recomputes from
// its payload and signature, and verifies against a keyless authority
// whose trusted root is signtest.TrustedRootJSON (what the live test puts
// in a trustedRootRef Secret).
func TestGitsign_GoGitCommitAndTrustedRoot(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	fs := memfs.New()
	repo, err := git.Init(memory.NewStorage(), fs)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	f, err := fs.Create("config.yaml")
	require.NoError(t, err)
	_, err = f.Write([]byte("signed\n"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = wt.Add("config.yaml")
	require.NoError(t, err)
	who := &object.Signature{Name: "Fixer", Email: "fixer@example.com", When: time.Now()}
	h, err := wt.Commit("fix", &git.CommitOptions{Author: who, Committer: who,
		Signer: signtest.GitsignSigner{VS: vs, Identity: gitsignIdentity, Issuer: gitsignIssuer}})
	require.NoError(t, err)
	c, err := repo.CommitObject(h)
	require.NoError(t, err)
	unsigned := &plumbing.MemoryObject{}
	require.NoError(t, c.EncodeWithoutSignature(unsigned))
	r, err := unsigned.Reader()
	require.NoError(t, err)
	payload, err := io.ReadAll(r)
	require.NoError(t, err)

	id, err := commitObjectID(payload, []byte(c.PGPSignature), false)
	require.NoError(t, err)
	assert.Equal(t, h.String(), id)

	raw, err := signtest.TrustedRootJSON(vs)
	require.NoError(t, err)
	tr, err := root.NewTrustedRootFromJSON(raw)
	require.NoError(t, err)
	vid, err := verify.NewShortCertificateIdentity(gitsignIssuer, "", gitsignIdentity, "")
	require.NoError(t, err)
	gc, err := parseGitsign(context.Background(), payload, []byte(c.PGPSignature))
	require.NoError(t, err)
	_, signer, err := authority{name: "agents", trusted: tr, identity: &vid}.verifyBundle(gc.bundle, gc.digest)
	require.NoError(t, err)
	assert.Equal(t, gitsignIdentity, signer)
}
