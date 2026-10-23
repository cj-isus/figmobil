// Package secure provides authenticated, recipient-bound age envelopes and
// Windows-local identity protection. It does not store files or trust peers.
package secure

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"filippo.io/age"
)

const (
	MaxPayloadBytes           int64 = 64 << 20
	MaxCiphertextBytes              = 96 << 20
	MaxLocalDataBytes               = 128 << 20
	MaxProtectedBytes               = MaxLocalDataBytes + 1<<20
	MaxIdentityProtectedBytes       = 16 << 10
	identityMagic                   = "figmobil-private-identity-v1\x00"
	signDomain                      = "figmobil.identity-signature.v1"
	sealDomain                      = "figmobil.sealed-payload.v1"
)

var (
	ErrIdentity         = errors.New("INVALID_PRIVATE_IDENTITY")
	ErrPublicKeys       = errors.New("INVALID_PUBLIC_KEYS")
	ErrSignature        = errors.New("INVALID_SIGNATURE")
	ErrCiphertext       = errors.New("INVALID_CIPHERTEXT")
	ErrSize             = errors.New("CRYPTO_SIZE_LIMIT")
	ErrEncryption       = errors.New("ENCRYPTION_FAILED")
	ErrProtection       = errors.New("LOCAL_PROTECTION_FAILED")
	ErrUnsupported      = errors.New("UNSUPPORTED_PLATFORM")
	ErrOwnerExists      = errors.New("DEVICE_OWNER_EXISTS")
	ErrOwnerUnavailable = errors.New("DEVICE_OWNER_UNAVAILABLE")
)

// PublicKeys is safe to transport. Both canonical keys constitute one identity;
// their initial trust must be established by the pairing protocol and user.
type PublicKeys struct {
	Encryption string `json:"encryption"`
	Signing    string `json:"signing"`
}

// Identity keeps private material in process memory. Persist only Protect's
// output; JSON serialization is deliberately rejected, including when nested.
type Identity struct {
	encryption *age.X25519Identity
	signing    ed25519.PrivateKey
}

func (*Identity) String() string               { return "figmobil identity (private)" }
func (*Identity) GoString() string             { return "figmobil identity (private)" }
func (*Identity) MarshalJSON() ([]byte, error) { return nil, ErrIdentity }

func Generate() (*Identity, error) {
	encryption, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, ErrIdentity
	}
	_, signing, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, ErrIdentity
	}
	return &Identity{encryption: encryption, signing: signing}, nil
}

func (i *Identity) valid() bool {
	return i != nil && i.encryption != nil && len(i.signing) == ed25519.PrivateKeySize
}

func (i *Identity) Public() PublicKeys {
	if !i.valid() {
		return PublicKeys{}
	}
	return PublicKeys{Encryption: i.encryption.Recipient().String(), Signing: base64.RawStdEncoding.EncodeToString(i.signing.Public().(ed25519.PublicKey))}
}

func decodeCanonical(value string, size int) ([]byte, error) {
	if len(value) > 256 {
		return nil, ErrPublicKeys
	}
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawStdEncoding.EncodeToString(decoded) != value {
		return nil, ErrPublicKeys
	}
	return decoded, nil
}

func ValidatePublic(keys PublicKeys) error {
	if len(keys.Encryption) > 256 {
		return ErrPublicKeys
	}
	recipient, err := age.ParseX25519Recipient(keys.Encryption)
	if err != nil || recipient.String() != keys.Encryption {
		return ErrPublicKeys
	}
	_, err = decodeCanonical(keys.Signing, ed25519.PublicKeySize)
	return err
}

// signatureInput is unambiguous length-prefixed protocol framing, not a crypto
// primitive. Each purpose has a distinct domain and binds complete key bundles.
func signatureInput(domain string, sender, recipient PublicKeys, payload []byte) []byte {
	var out bytes.Buffer
	for _, part := range [][]byte{[]byte(domain), []byte(sender.Encryption), []byte(sender.Signing), []byte(recipient.Encryption), []byte(recipient.Signing), payload} {
		_ = binary.Write(&out, binary.BigEndian, uint64(len(part)))
		_, _ = out.Write(part)
	}
	return out.Bytes()
}

// Sign binds the signer bundle and a distinct protocol domain. The caller's
// typed payload must include kind/version, project, recipient and transcript IDs
// as applicable; this primitive does not establish trust or prevent replay.
func (i *Identity) Sign(payload []byte) (string, error) {
	if !i.valid() {
		return "", ErrIdentity
	}
	if int64(len(payload)) > MaxPayloadBytes {
		return "", ErrSize
	}
	signature := ed25519.Sign(i.signing, signatureInput(signDomain, i.Public(), PublicKeys{}, payload))
	return base64.RawStdEncoding.EncodeToString(signature), nil
}

func verifyDomain(keys, recipient PublicKeys, domain string, payload []byte, signature string) error {
	if err := ValidatePublic(keys); err != nil {
		return err
	}
	if int64(len(payload)) > MaxPayloadBytes {
		return ErrSize
	}
	public, _ := decodeCanonical(keys.Signing, ed25519.PublicKeySize)
	signed, err := decodeCanonical(signature, ed25519.SignatureSize)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(public), signatureInput(domain, keys, recipient, payload), signed) {
		return ErrSignature
	}
	return nil
}

func Verify(keys PublicKeys, payload []byte, signature string) error {
	return verifyDomain(keys, PublicKeys{}, signDomain, payload, signature)
}

type sealedEnvelope struct {
	Version   int        `json:"version"`
	Sender    PublicKeys `json:"sender"`
	Recipient PublicKeys `json:"recipient"`
	Payload   []byte     `json:"payload"`
	Signature string     `json:"signature"`
}

func (i *Identity) Seal(recipient PublicKeys, payload []byte) ([]byte, error) {
	if !i.valid() {
		return nil, ErrIdentity
	}
	if err := ValidatePublic(recipient); err != nil {
		return nil, err
	}
	if int64(len(payload)) > MaxPayloadBytes {
		return nil, ErrSize
	}
	sender := i.Public()
	envelope := sealedEnvelope{Version: 1, Sender: sender, Recipient: recipient, Payload: payload}
	envelope.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(i.signing, signatureInput(sealDomain, sender, recipient, payload)))
	plaintext, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrEncryption
	}
	defer clear(plaintext)
	key, _ := age.ParseX25519Recipient(recipient.Encryption)
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, key)
	if err != nil {
		return nil, ErrEncryption
	}
	if _, err := writer.Write(plaintext); err != nil {
		return nil, ErrEncryption
	}
	if err := writer.Close(); err != nil {
		return nil, ErrEncryption
	}
	return ciphertext.Bytes(), nil
}

// Open returns payload only after bounded reading reaches the authenticated end
// of the entire age stream and the expected sender's recipient-bound signature
// verifies. An authenticated plaintext prefix never leaves this function.
func (i *Identity) Open(sender PublicKeys, ciphertext []byte, maxPlaintext int64) ([]byte, error) {
	if !i.valid() {
		return nil, ErrIdentity
	}
	if err := ValidatePublic(sender); err != nil {
		return nil, err
	}
	if maxPlaintext <= 0 || maxPlaintext > MaxPayloadBytes || len(ciphertext) > MaxCiphertextBytes {
		return nil, ErrSize
	}
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), i.encryption)
	if err != nil {
		return nil, ErrCiphertext
	}
	// JSON base64 plus bounded key/signature/field overhead. The +1 allows us
	// to distinguish a too-large stream without accepting a truncated prefix.
	maxEnvelope := (maxPlaintext+2)/3*4 + 4096
	plaintext, err := io.ReadAll(io.LimitReader(reader, maxEnvelope+1))
	defer clear(plaintext)
	if err != nil {
		return nil, ErrCiphertext
	}
	if int64(len(plaintext)) > maxEnvelope {
		return nil, ErrSize
	}
	var envelope sealedEnvelope
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(new(any)) != io.EOF {
		clear(envelope.Payload)
		return nil, ErrCiphertext
	}
	if envelope.Version != 1 || envelope.Sender != sender || envelope.Recipient != i.Public() {
		clear(envelope.Payload)
		return nil, ErrSignature
	}
	if int64(len(envelope.Payload)) > maxPlaintext {
		clear(envelope.Payload)
		return nil, ErrSize
	}
	if err := verifyDomain(sender, envelope.Recipient, sealDomain, envelope.Payload, envelope.Signature); err != nil {
		clear(envelope.Payload)
		return nil, err
	}
	return envelope.Payload, nil
}

func (i *Identity) Protect() ([]byte, error) {
	if !i.valid() {
		return nil, ErrIdentity
	}
	secret := []byte(i.encryption.String())
	defer clear(secret)
	seed := i.signing.Seed()
	defer clear(seed)
	plaintext := make([]byte, len(identityMagic)+2+len(secret)+len(seed))
	defer clear(plaintext)
	copy(plaintext, identityMagic)
	binary.BigEndian.PutUint16(plaintext[len(identityMagic):], uint16(len(secret)))
	copy(plaintext[len(identityMagic)+2:], secret)
	copy(plaintext[len(identityMagic)+2+len(secret):], seed)
	return ProtectData(plaintext)
}

func Restore(protected []byte) (*Identity, error) {
	if len(protected) > MaxIdentityProtectedBytes {
		return nil, ErrSize
	}
	plaintext, err := UnprotectData(protected)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	if len(plaintext) < len(identityMagic)+2+ed25519.SeedSize || !bytes.HasPrefix(plaintext, []byte(identityMagic)) {
		return nil, ErrIdentity
	}
	length := int(binary.BigEndian.Uint16(plaintext[len(identityMagic):]))
	if length == 0 || length > 256 || len(plaintext) != len(identityMagic)+2+length+ed25519.SeedSize {
		return nil, ErrIdentity
	}
	secret := plaintext[len(identityMagic)+2 : len(identityMagic)+2+length]
	encryption, err := age.ParseX25519Identity(string(secret))
	if err != nil {
		return nil, ErrIdentity
	}
	seed := plaintext[len(identityMagic)+2+length:]
	return &Identity{encryption: encryption, signing: ed25519.NewKeyFromSeed(seed)}, nil
}
