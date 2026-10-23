package channel

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"figmobil/internal/compare"
	"figmobil/internal/secure"
)

type wire struct {
	Version   int               `json:"version"`
	Kind      string            `json:"kind"`
	ProjectID string            `json:"projectId"`
	InviteID  string            `json:"inviteId"`
	Keys      secure.PublicKeys `json:"keys"`
	Body      []byte            `json:"body"`
	Signature string            `json:"signature"`
}
type invitation struct {
	DeviceName string `json:"deviceName"`
}
type reply struct {
	DeviceName string            `json:"deviceName"`
	InviteHash string            `json:"inviteHash"`
	Inviter    secure.PublicKeys `json:"inviter"`
}
type packet struct {
	Version    int               `json:"version"`
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	ProjectID  string            `json:"projectId"`
	InviteID   string            `json:"inviteId"`
	Transcript string            `json:"transcript"`
	BaseID     string            `json:"baseId"`
	ReplyTo    string            `json:"replyTo"`
	AckOf      string            `json:"ackOf"`
	AcceptedID string            `json:"acceptedId"`
	Manifest   *compare.Manifest `json:"manifest,omitempty"`
	Files      map[string][]byte `json:"files"`
	Deletes    []string          `json:"deletes,omitempty"`
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
func strictJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > MaxMessageBytes || !utf8.Valid(data) {
		return errors.New("JSON size")
	}
	preflight := json.NewDecoder(bytes.NewReader(data))
	members := 0
	if err := jsonShape(preflight, 0, &members); err != nil {
		return err
	}
	if _, err := preflight.Token(); err != io.EOF {
		return errors.New("JSON trailing content")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("JSON format")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("JSON trailing content")
	}
	return nil
}

// Bound collection allocations before decoding an untrusted signed package.
// The application never needs arbitrary-depth JSON or duplicate object keys.
func jsonShape(decoder *json.Decoder, depth int, members *int) error {
	if depth > 32 {
		return errors.New("JSON depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("JSON token")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errors.New("JSON delimiter")
	}
	keys := map[string]bool{}
	count := 0
	for decoder.More() {
		count++
		*members++
		if count > 5000 || *members > 80000 {
			return errors.New("JSON member limit")
		}
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return errors.New("JSON key")
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("JSON duplicate key")
			}
			keys[name] = true
		}
		if err := jsonShape(decoder, depth+1, members); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	expected := json.Delim(']')
	if delimiter == '{' {
		expected = '}'
	}
	if err != nil || end != expected {
		return errors.New("JSON end")
	}
	return nil
}
func publicWire(identity *secure.Identity, kind, projectID, inviteID string, body any) ([]byte, error) {
	content, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	w := wire{Version: 1, Kind: kind, ProjectID: projectID, InviteID: inviteID, Keys: identity.Public(), Body: content}
	unsigned, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	w.Signature, err = identity.Sign(unsigned)
	if err != nil {
		return nil, err
	}
	return json.Marshal(w)
}
func verifyPublic(w wire) error {
	if w.Version != 1 || !hexID(w.ProjectID, 32) || !hexID(w.InviteID, 32) || len(w.Body) > 16384 || (w.Kind != "invite" && w.Kind != "reply") {
		return errors.New("public envelope")
	}
	sig := w.Signature
	w.Signature = ""
	unsigned, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return secure.Verify(w.Keys, unsigned, sig)
}
func verificationCode(p *profile, identity *secure.Identity) string {
	a, b := identity.Public(), p.Peer
	aName, bName := p.DeviceName, p.PeerName
	if !p.Originator {
		a, b = b, a
		aName, bName = bName, aName
	}
	transcript := struct {
		Domain, ProjectID, InviteID, InviteHash string
		A, B                                    secure.PublicKeys
		AName, BName                            string
	}{"figmobil/pairing/v1/age-and-ed25519", p.ID, p.InviteID, digest(p.Offer), a, b, aName, bName}
	data, _ := json.Marshal(transcript)
	raw := strings.ToUpper(digest(data)[:32])
	groups := make([]string, 0, 8)
	for i := 0; i < len(raw); i += 4 {
		groups = append(groups, raw[i:i+4])
	}
	return strings.Join(groups, "-")
}
func sealedWire(p *profile, identity *secure.Identity, msg *packet) ([]byte, error) {
	msg.Version = 1
	msg.ProjectID = p.ID
	msg.InviteID = p.InviteID
	msg.Transcript = verificationCode(p, identity)
	var err error
	msg.ID, err = randomID()
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(msg)
	if err != nil || len(plain) > MaxPlainBytes {
		return nil, errors.New("package size")
	}
	defer clear(plain)
	ciphertext, err := identity.Seal(p.Peer, plain)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(wire{Version: 1, Kind: "sealed", ProjectID: p.ID, InviteID: p.InviteID, Body: ciphertext})
	if err != nil || len(data) > MaxMessageBytes {
		return nil, errors.New("encrypted package size")
	}
	return data, nil
}
