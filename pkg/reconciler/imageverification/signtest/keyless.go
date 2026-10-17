// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package signtest

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// KeylessAttest is an attestation bundle for repository@digest signed the
// keyless way by vs, a private Sigstore: a Fulcio leaf certificate for
// identity and issuer, a Rekor entry with its inclusion proof and an RFC 3161
// timestamp, like `cosign attest --new-bundle-format` against that Sigstore.
// It verifies offline against TrustedRootJSON(vs).
func KeylessAttest(vs *ca.VirtualSigstore, identity, issuer, repository, digest, predicateType string, predicate interface{}) ([]byte, error) {
	hexPart, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return nil, fmt.Errorf("digest %q is not sha256", digest)
	}
	statement, err := json.Marshal(map[string]interface{}{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []interface{}{map[string]interface{}{"name": repository, "digest": map[string]string{"sha256": hexPart}}},
		"predicateType": predicateType,
		"predicate":     predicate,
	})
	if err != nil {
		return nil, err
	}
	// VirtualSigstore's leaf certificates are valid from a few minutes
	// ahead; its own Attest logs the entry at that time.
	entity, err := vs.AttestAtTime(identity, issuer, statement, time.Now().Add(5*time.Minute), true)
	if err != nil {
		return nil, fmt.Errorf("attest: %w", err)
	}
	vc, err := entity.VerificationContent()
	if err != nil {
		return nil, err
	}
	cert := vc.Certificate()
	if cert == nil {
		return nil, fmt.Errorf("attest: no leaf certificate")
	}
	entries, err := entity.TlogEntries()
	if err != nil {
		return nil, err
	}
	var tles []*rekorpb.TransparencyLogEntry
	for _, e := range entries {
		src := e.TransparencyLogEntry()
		if src == nil {
			return nil, fmt.Errorf("attest: a log entry without its protobuf form")
		}
		tle := proto.Clone(src).(*rekorpb.TransparencyLogEntry)
		// tlog.NewEntry leaves out the kind and the signed entry timestamp,
		// which a bundle carries: VirtualSigstore logs a dsse 0.0.1 entry,
		// and its Rekor key signs the promise again.
		tle.KindVersion = &rekorpb.KindVersion{Kind: "dsse", Version: "0.0.1"}
		set, err := vs.RekorSignPayload(tlog.RekorPayload{Body: base64.StdEncoding.EncodeToString(tle.CanonicalizedBody),
			IntegratedTime: tle.IntegratedTime, LogIndex: tle.LogIndex, LogID: hex.EncodeToString(tle.LogId.GetKeyId())})
		if err != nil {
			return nil, fmt.Errorf("attest: signed entry timestamp: %w", err)
		}
		tle.InclusionPromise = &rekorpb.InclusionPromise{SignedEntryTimestamp: set}
		tles = append(tles, tle)
	}
	stamps, err := entity.Timestamps()
	if err != nil {
		return nil, err
	}
	var rfc3161 []*protocommon.RFC3161SignedTimestamp
	for _, s := range stamps {
		rfc3161 = append(rfc3161, &protocommon.RFC3161SignedTimestamp{SignedTimestamp: s})
	}
	sc, err := entity.SignatureContent()
	if err != nil {
		return nil, err
	}
	env := sc.EnvelopeContent()
	if env == nil {
		return nil, fmt.Errorf("attest: no DSSE envelope")
	}
	raw := env.RawEnvelope()
	var sigs []*protodsse.Signature
	for _, s := range raw.Signatures {
		b, err := decodeB64(s.Sig)
		if err != nil {
			return nil, fmt.Errorf("attest: signature: %w", err)
		}
		sigs = append(sigs, &protodsse.Signature{Sig: b, Keyid: s.KeyID})
	}
	payload, err := decodeB64(raw.Payload)
	if err != nil {
		return nil, fmt.Errorf("attest: payload: %w", err)
	}
	pb := &protobundle.Bundle{
		MediaType: BundleMediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content:                   &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: cert.Raw}},
			TlogEntries:               tles,
			TimestampVerificationData: &protobundle.TimestampVerificationData{Rfc3161Timestamps: rfc3161},
		},
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload: payload, PayloadType: raw.PayloadType, Signatures: sigs,
		}},
	}
	return protojson.Marshal(pb)
}

// decodeB64 decodes standard base64, as DSSE envelopes hold.
func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
