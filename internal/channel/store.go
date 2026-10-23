package channel

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"figmobil/internal/compare"
	"figmobil/internal/secure"
)

const maxProfiles = 8
const maxRetainedBytes = 128 << 20

type outgoing struct {
	Kind        string            `json:"kind"`
	Message     []byte            `json:"message"`
	Digest      string            `json:"digest"`
	Next        int               `json:"next"`
	AwaitingAck bool              `json:"awaitingAck"`
	PacketID    string            `json:"packetId"`
	Manifest    *compare.Manifest `json:"manifest,omitempty"`
	BaseID      string            `json:"baseId"`
	ReplyTo     string            `json:"replyTo"`
}
type profile struct {
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	DeviceName    string            `json:"deviceName"`
	Root          string            `json:"root"`
	Excludes      []string          `json:"excludes"`
	Originator    bool              `json:"originator"`
	InviteID      string            `json:"inviteId"`
	Offer         []byte            `json:"offer"`
	Peer          secure.PublicKeys `json:"peer"`
	PeerName      string            `json:"peerName"`
	Private       []byte            `json:"private"`
	Confirmed     bool              `json:"confirmed"`
	PeerConfirmed bool              `json:"peerConfirmed"`
	Base          *compare.Manifest `json:"base,omitempty"`
	Outgoing      *outgoing         `json:"outgoing,omitempty"`
	Inbox         *assembly         `json:"inbox,omitempty"`
	Pending       *packet           `json:"pending,omitempty"`
	PendingDigest string            `json:"pendingDigest,omitempty"`
	MergeRequired bool              `json:"mergeRequired"`
	History       []string          `json:"history"`
}

func (m *Manager) save(p *profile) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(data) > secure.MaxLocalDataBytes {
		return errors.New("profile size limit")
	}
	defer clear(data)
	protected, err := secure.ProtectData(data)
	if err != nil {
		return err
	}
	path := filepath.Join(m.dir, p.ID+".fmstate")
	file, err := os.CreateTemp(m.dir, ".state-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(protected)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (m *Manager) load() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".fmstate") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".fmstate")
		if !hexID(id, 32) || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("invalid profile file")
		}
		if len(m.profiles) >= maxProfiles {
			return errors.New("profile limit")
		}
		file, err := os.Open(filepath.Join(m.dir, entry.Name()))
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if info.Size() > secure.MaxProtectedBytes {
			file.Close()
			return errors.New("profile size")
		}
		protected, err := io.ReadAll(io.LimitReader(file, secure.MaxProtectedBytes+1))
		file.Close()
		if err != nil || len(protected) > secure.MaxProtectedBytes {
			return errors.New("profile read limit")
		}
		data, err := secure.UnprotectData(protected)
		if err != nil {
			return errors.New("profile protection")
		}
		var p profile
		if err = json.Unmarshal(data, &p); err != nil || p.Version != 1 || p.ID != id || !hexID(p.InviteID, 32) || len(p.Private) == 0 {
			return errors.New("profile format")
		}
		clear(data)
		if _, err = secure.Restore(p.Private); err != nil {
			return errors.New("profile keys")
		}
		if !filepath.IsAbs(p.Root) || rootOverlaps(p.Root, m.dir) || !validLabel(p.Name) || !validLabel(p.DeviceName) {
			return errors.New("profile binding")
		}
		if p.Peer.Encryption != "" {
			if secure.ValidatePublic(p.Peer) != nil || !validLabel(p.PeerName) {
				return errors.New("profile peer")
			}
		} else if p.Confirmed || p.PeerConfirmed {
			return errors.New("profile trust")
		}
		if len(p.History) > 128 {
			return errors.New("profile history")
		}
		for _, id := range p.History {
			if !hexID(id, 64) {
				return errors.New("profile replay record")
			}
		}
		if p.Base != nil {
			expected, err := compare.ManifestID(*p.Base)
			if err != nil || expected != p.Base.ID {
				return errors.New("profile base")
			}
		}
		if p.Pending != nil {
			if p.Pending.Manifest == nil || !hexID(p.PendingDigest, 64) || p.Pending.Type != "data" || p.Pending.ProjectID != p.ID || p.Pending.InviteID != p.InviteID || compare.ValidateSnapshot(compare.Snapshot{Manifest: *p.Pending.Manifest, Files: p.Pending.Files}) != nil {
				return errors.New("profile pending package")
			}
		}
		if p.Inbox != nil {
			if !hexID(p.Inbox.ID, 64) || p.Inbox.Total < 1 || p.Inbox.Total > maxParts || len(p.Inbox.Chunks) > p.Inbox.Total {
				return errors.New("saved inbox")
			}
			size := 0
			for index, chunk := range p.Inbox.Chunks {
				if index < 1 || index > p.Inbox.Total || len(chunk) == 0 || len(chunk) > rawPartBytes {
					return errors.New("saved chunk")
				}
				size += len(chunk)
			}
			if size != p.Inbox.Bytes || size > MaxMessageBytes {
				return errors.New("saved inbox size")
			}
		}
		if p.Outgoing != nil && (len(p.Outgoing.Message) == 0 || len(p.Outgoing.Message) > MaxMessageBytes || p.Outgoing.Next < 0 || p.Outgoing.Next > partCount(p.Outgoing.Message)) {
			return errors.New("saved outbox")
		}
		if p.Outgoing != nil {
			if p.Outgoing.AwaitingAck && (p.Outgoing.Kind != "data" || !hexID(p.Outgoing.PacketID, 32) || p.Outgoing.Manifest == nil) {
				return errors.New("saved outgoing acknowledgement")
			}
			expected := digest(p.Outgoing.Message)
			if p.Outgoing.Digest != "" && p.Outgoing.Digest != expected {
				return errors.New("saved outbox integrity")
			}
			p.Outgoing.Digest = expected
		}
		m.profiles[p.ID] = &p
	}
	return m.checkRetained()
}
func (m *Manager) checkRetained() error {
	size := 0
	for _, p := range m.profiles {
		if p.Outgoing != nil {
			size += len(p.Outgoing.Message)
		}
		if p.Inbox != nil {
			size += p.Inbox.Bytes
		}
		if p.Pending != nil {
			for _, data := range p.Pending.Files {
				size += len(data)
			}
		}
	}
	if size > m.retainedLimit {
		return errors.New("retained package limit")
	}
	return nil
}
func rootOverlaps(root, dir string) bool {
	a, err1 := filepath.Abs(root)
	b, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}
