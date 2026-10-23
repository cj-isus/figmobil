package secure

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"filippo.io/age"
)

func identityForTest(t *testing.T) *Identity {
	t.Helper()
	identity, err := Generate()
	if err != nil {
		t.Fatal("synthetic identity generation failed:", err)
	}
	return identity
}

func encryptEnvelopeForTest(t *testing.T, recipient PublicKeys, envelope sealedEnvelope) []byte {
	t.Helper()
	key, err := age.ParseX25519Recipient(recipient.Encryption)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	var output bytes.Buffer
	writer, err := age.Encrypt(&output, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestSignaturesBindPayloadAndCompletePublicIdentity(t *testing.T) {
	a, other := identityForTest(t), identityForTest(t)
	payload := []byte("synthetic invitation transcript")
	signature, err := a.Sign(payload)
	if err != nil || Verify(a.Public(), payload, signature) != nil {
		t.Fatal("valid signature did not verify", err)
	}
	if Verify(other.Public(), payload, signature) == nil || Verify(a.Public(), []byte("different"), signature) == nil {
		t.Fatal("signature accepted wrong sender or changed payload")
	}
	swapped := a.Public()
	swapped.Encryption = other.Public().Encryption
	if Verify(swapped, payload, signature) == nil {
		t.Fatal("encryption-key substitution preserved a signing-key signature")
	}
	if Verify(a.Public(), payload, signature+"\n") == nil || Verify(a.Public(), payload, "invalid") == nil {
		t.Fatal("noncanonical or malformed signature accepted")
	}
	for _, keys := range []PublicKeys{{}, {Encryption: a.Public().Encryption, Signing: "bad"}, {Encryption: "age1invalid", Signing: a.Public().Signing}} {
		if !errors.Is(ValidatePublic(keys), ErrPublicKeys) {
			t.Fatal("invalid public key accepted")
		}
	}
}

func TestSealRoundtripFreshRandomnessAndRecipientBinding(t *testing.T) {
	a, b, other := identityForTest(t), identityForTest(t), identityForTest(t)
	payload := []byte("synthetic source\r\n\x00opaque bytes\xff")
	first, err := a.Seal(b.Public(), payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Seal(b.Public(), payload)
	if err != nil || bytes.Equal(first, second) || bytes.Contains(first, payload) {
		t.Fatal("encryption randomness or confidentiality regression", err)
	}
	opened, err := b.Open(a.Public(), first, int64(len(payload)))
	if err != nil || !bytes.Equal(opened, payload) {
		t.Fatal("roundtrip failed", err)
	}
	if output, err := other.Open(a.Public(), first, 1024); err == nil || output != nil {
		t.Fatal("wrong recipient received plaintext")
	}
	if output, err := b.Open(other.Public(), first, 1024); err == nil || output != nil {
		t.Fatal("wrong expected sender received plaintext")
	}
	// Changing only the recipient's signing key must fail, even when the same
	// age private key can decrypt the ciphertext.
	modifiedRecipient := b.Public()
	modifiedRecipient.Signing = other.Public().Signing
	wrongBinding, err := a.Seal(modifiedRecipient, payload)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := b.Open(a.Public(), wrongBinding, 1024); err == nil || output != nil {
		t.Fatal("recipient key bundle was not bound")
	}
}

func TestOpenRejectsTamperingTruncationTrailingDataAndLimits(t *testing.T) {
	a, b := identityForTest(t), identityForTest(t)
	payload := bytes.Repeat([]byte("synthetic"), 10000)
	ciphertext, err := a.Seal(b.Public(), payload)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	for name, invalid := range map[string][]byte{
		"empty": nil, "header": ciphertext[:16], "middle": ciphertext[:len(ciphertext)/2],
		"final-tag": ciphertext[:len(ciphertext)-1], "tampered": tampered, "trailing": append(append([]byte(nil), ciphertext...), 0),
	} {
		t.Run(name, func(t *testing.T) {
			if output, err := b.Open(a.Public(), invalid, int64(len(payload))); err == nil || output != nil {
				t.Fatal("unauthenticated plaintext escaped")
			}
		})
	}
	for _, maximum := range []int64{-1, 0, 1, int64(len(payload)) - 1, MaxPayloadBytes + 1} {
		if output, err := b.Open(a.Public(), ciphertext, maximum); !errors.Is(err, ErrSize) || output != nil {
			t.Fatalf("plaintext size bound %d bypassed: %v", maximum, err)
		}
	}
}

func TestAgeRecipientEncryptionAloneDoesNotAuthenticateSender(t *testing.T) {
	a, b, attacker := identityForTest(t), identityForTest(t), identityForTest(t)
	payload := []byte("synthetic forged payload")
	forged := sealedEnvelope{Version: 1, Sender: a.Public(), Recipient: b.Public(), Payload: payload}
	forged.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(attacker.signing, signatureInput(sealDomain, a.Public(), b.Public(), payload)))
	ciphertext := encryptEnvelopeForTest(t, b.Public(), forged)
	if output, err := b.Open(a.Public(), ciphertext, 1024); !errors.Is(err, ErrSignature) || output != nil {
		t.Fatal("public recipient encryption masqueraded as trusted sender", err)
	}
	forged.Signature, _ = a.Sign(payload)
	ciphertext = encryptEnvelopeForTest(t, b.Public(), forged)
	if output, err := b.Open(a.Public(), ciphertext, 1024); !errors.Is(err, ErrSignature) || output != nil {
		t.Fatal("generic signature crossed sealed-envelope domain", err)
	}
}

func TestOpenLimitBoundariesAreCheckedBeforeCiphertextParsing(t *testing.T) {
	a, b := identityForTest(t), identityForTest(t)
	for _, maximum := range []int64{0, -1, MaxPayloadBytes + 1} {
		if payload, err := b.Open(a.Public(), nil, maximum); !errors.Is(err, ErrSize) || payload != nil {
			t.Fatal("invalid limit was not rejected before parsing", err)
		}
	}
	// A maximum allowance does not allocate a maximum-sized buffer. An empty
	// ciphertext reaches the format check and is rejected as ciphertext instead.
	if payload, err := b.Open(a.Public(), nil, MaxPayloadBytes); !errors.Is(err, ErrCiphertext) || payload != nil {
		t.Fatal("exact maximum allowance was incorrectly rejected", err)
	}
}

func TestIdentityCannotBeSerializedOrAccidentallyFormatted(t *testing.T) {
	identity := identityForTest(t)
	if data, err := json.Marshal(identity); err == nil || len(data) != 0 {
		t.Fatal("private identity serialized to JSON")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if output := fmt.Sprintf(format, identity); output != "figmobil identity (private)" {
			t.Fatal("private identity formatting was not redacted")
		}
	}
	var invalid *Identity
	if _, err := invalid.Sign(nil); !errors.Is(err, ErrIdentity) {
		t.Fatal("invalid identity accepted")
	}
	if err := ValidatePublic(identity.Public()); err != nil {
		t.Fatal(err)
	}
}
