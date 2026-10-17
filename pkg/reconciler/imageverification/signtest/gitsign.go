// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package signtest

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"time"

	"github.com/digitorus/pkcs7"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag/conv"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/rekor/pkg/generated/models"
	"github.com/sigstore/rekor/pkg/types"
	"github.com/sigstore/rekor/pkg/types/hashedrekord"
	hashedrekordv001 "github.com/sigstore/rekor/pkg/types/hashedrekord/v0.0.1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"google.golang.org/protobuf/proto"
)

// oidRekorTransparencyLogEntry is gitsign's unsigned attribute holding the
// serialized Rekor TransparencyLogEntry.
var oidRekorTransparencyLogEntry = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 3, 1}

// GitsignOptions shape a test gitsign signature.
type GitsignOptions struct {
	// At is the Rekor integrated time; zero is now.
	At time.Time
	// NoEntry leaves the Rekor entry out (gitsign's old online mode).
	NoEntry bool
	// Proof adds an inclusion proof with a checkpoint.
	Proof bool
	// Entry, when set, is embedded instead of the signature's own entry.
	Entry *rekorpb.TransparencyLogEntry
}

// GitsignSign signs payload (a git commit object without its signature
// header) the way gitsign does, with a leaf certificate of vs for identity
// and issuer: a detached CMS SignedData, PEM "SIGNED MESSAGE", with its
// Rekor hashedrekord entry embedded as a serialized TransparencyLogEntry
// (its body left out) in an unsigned attribute. It returns the signature
// and the entry it embedded.
func GitsignSign(vs *ca.VirtualSigstore, identity, issuer string, payload []byte, o GitsignOptions) ([]byte, *rekorpb.TransparencyLogEntry, error) {
	cert, key, err := vs.GenerateLeafCert(identity, issuer)
	if err != nil {
		return nil, nil, err
	}
	sd, err := pkcs7.NewSignedData(payload)
	if err != nil {
		return nil, nil, err
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSigner(cert, key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, nil, err
	}
	sd.Detach()
	si := &sd.GetSignedData().SignerInfos[0]
	var tle *rekorpb.TransparencyLogEntry
	if !o.NoEntry {
		signed, err := signedAttributes(si.AuthenticatedAttributes)
		if err != nil {
			return nil, nil, err
		}
		digest := sha256.Sum256(signed)
		body, err := hashedRekordBody(digest[:], si.EncryptedDigest, cert)
		if err != nil {
			return nil, nil, err
		}
		if tle, err = logEntry(vs, body, o); err != nil {
			return nil, nil, err
		}
		if o.Entry != nil {
			tle = o.Entry
		}
		raw, err := proto.Marshal(tle)
		if err != nil {
			return nil, nil, err
		}
		if err := si.SetUnauthenticatedAttributes([]pkcs7.Attribute{{Type: oidRekorTransparencyLogEntry, Value: raw}}); err != nil {
			return nil, nil, err
		}
	}
	der, err := sd.Finish()
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "SIGNED MESSAGE", Bytes: der}), tle, nil
}

// GitsignSigner is a go-git Signer (git.CommitOptions.Signer) that signs
// commits as GitsignSign does, with an inclusion proof.
type GitsignSigner struct {
	VS               *ca.VirtualSigstore
	Identity, Issuer string
}

// Sign implements go-git's Signer.
func (s GitsignSigner) Sign(message io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(message)
	if err != nil {
		return nil, err
	}
	sig, _, err := GitsignSign(s.VS, s.Identity, s.Issuer, payload, GitsignOptions{Proof: true})
	return sig, err
}

// TrustedRootJSON is vs's trusted root as a Sigstore trusted_root.json, for
// a keyless authority's trustedRootRef.
func TrustedRootJSON(vs *ca.VirtualSigstore) ([]byte, error) {
	rekor, err := rawLogIDs(vs.RekorLogs())
	if err != nil {
		return nil, err
	}
	ct, err := rawLogIDs(vs.CTLogs())
	if err != nil {
		return nil, err
	}
	tr, err := root.NewTrustedRoot(root.TrustedRootMediaType01, vs.FulcioCertificateAuthorities(), ct,
		vs.TimestampingAuthorities(), rekor)
	if err != nil {
		return nil, err
	}
	return tr.MarshalJSON()
}

// rawLogIDs are VirtualSigstore's logs with their IDs as the raw key IDs
// trusted_root.json holds (VirtualSigstore keeps the hex string's bytes),
// valid from an hour ago with no end.
func rawLogIDs(logs map[string]*root.TransparencyLog) (map[string]*root.TransparencyLog, error) {
	out := make(map[string]*root.TransparencyLog, len(logs))
	for k, l := range logs {
		c := *l
		id, err := hex.DecodeString(string(l.ID))
		if err != nil {
			return nil, fmt.Errorf("log %s: %w", k, err)
		}
		c.ID, c.ValidityPeriodStart, c.ValidityPeriodEnd = id, time.Now().Add(-time.Hour), time.Time{}
		out[k] = &c
	}
	return out, nil
}

// logEntry is vs's log entry for body, with its signed entry timestamp and,
// when o.Proof, an inclusion proof. Its body is left out, as gitsign
// stores it.
func logEntry(vs *ca.VirtualSigstore, body []byte, o GitsignOptions) (*rekorpb.TransparencyLogEntry, error) {
	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	logID, err := vs.RekorLogID()
	if err != nil {
		return nil, err
	}
	rawID, err := hex.DecodeString(logID)
	if err != nil {
		return nil, err
	}
	set, err := vs.RekorSignPayload(tlog.RekorPayload{
		Body: base64.StdEncoding.EncodeToString(body), IntegratedTime: at.Unix(), LogIndex: 1000, LogID: logID,
	})
	if err != nil {
		return nil, err
	}
	tle := &rekorpb.TransparencyLogEntry{
		LogIndex:         1000,
		LogId:            &protocommon.LogId{KeyId: rawID},
		KindVersion:      &rekorpb.KindVersion{Kind: hashedrekord.KIND, Version: hashedrekordv001.APIVERSION},
		IntegratedTime:   at.Unix(),
		InclusionPromise: &rekorpb.InclusionPromise{SignedEntryTimestamp: set},
	}
	if o.Proof {
		p, err := vs.GetInclusionProof(body)
		if err != nil {
			return nil, err
		}
		rootHash, err := hex.DecodeString(*p.RootHash)
		if err != nil {
			return nil, err
		}
		var hashes [][]byte
		for _, h := range p.Hashes {
			b, err := hex.DecodeString(h)
			if err != nil {
				return nil, err
			}
			hashes = append(hashes, b)
		}
		tle.InclusionProof = &rekorpb.InclusionProof{LogIndex: *p.LogIndex, RootHash: rootHash, TreeSize: *p.TreeSize,
			Hashes: hashes, Checkpoint: &rekorpb.Checkpoint{Envelope: *p.Checkpoint}}
	}
	return tle, nil
}

type cmsAttribute struct {
	Type  asn1.ObjectIdentifier
	Value asn1.RawValue `asn1:"set"`
}

// signedAttributes is the DER SET OF attrs (RFC 5652 §5.4).
func signedAttributes[T any](attrs []T) ([]byte, error) {
	out := make([]cmsAttribute, 0, len(attrs))
	for _, a := range attrs {
		raw, err := asn1.Marshal(a)
		if err != nil {
			return nil, err
		}
		var c cmsAttribute
		if _, err := asn1.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	seq, err := asn1.Marshal(struct {
		A []cmsAttribute `asn1:"set"`
	}{A: out})
	if err != nil {
		return nil, err
	}
	var outer asn1.RawValue
	if _, err := asn1.Unmarshal(seq, &outer); err != nil {
		return nil, err
	}
	return outer.Bytes, nil
}

func hashedRekordBody(digest, sig []byte, cert *x509.Certificate) ([]byte, error) {
	certPEM, err := cryptoutils.MarshalCertificateToPEM(cert)
	if err != nil {
		return nil, err
	}
	e := &hashedrekordv001.V001Entry{HashedRekordObj: models.HashedrekordV001Schema{
		Data: &models.HashedrekordV001SchemaData{Hash: &models.HashedrekordV001SchemaDataHash{
			Algorithm: conv.Pointer(models.HashedrekordV001SchemaDataHashAlgorithmSha256),
			Value:     conv.Pointer(hex.EncodeToString(digest)),
		}},
		Signature: &models.HashedrekordV001SchemaSignature{
			Content:   strfmt.Base64(sig),
			PublicKey: &models.HashedrekordV001SchemaSignaturePublicKey{Content: strfmt.Base64(certPEM)},
		},
	}}
	body, err := types.CanonicalizeEntry(context.Background(), e)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	return body, nil
}
