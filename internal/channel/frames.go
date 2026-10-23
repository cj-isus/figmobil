// Package channel implements the explicit, offline text-package workflow.
package channel

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	MaxMessageBytes = 96 << 20
	MaxPlainBytes   = 64 << 20
	rawPartBytes    = 180000
	maxParts        = (MaxMessageBytes + rawPartBytes - 1) / rawPartBytes
)

type assembly struct {
	ID     string         `json:"id"`
	Total  int            `json:"total"`
	Chunks map[int][]byte `json:"chunks"`
	Bytes  int            `json:"bytes"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func hexID(s string, length int) bool {
	if len(s) != length {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}
func partCount(data []byte) int { return (len(data) + rawPartBytes - 1) / rawPartBytes }

// frameAt materializes one part only; callers must acquire their shared gate
// immediately before handing this text to the local HTTP response.
func frameAt(data []byte, index int) (string, error) {
	return frameAtID(data, digest(data), index)
}
func frameAtID(data []byte, id string, index int) (string, error) {
	if len(data) == 0 || len(data) > MaxMessageBytes || index < 1 || index > partCount(data) || !hexID(id, 64) {
		return "", errors.New("invalid outgoing part")
	}
	start := (index - 1) * rawPartBytes
	end := min(start+rawPartBytes, len(data))
	chunk := data[start:end]
	return fmt.Sprintf("FIGMOBIL/1 %s %d %d %s\n%s", id, index, partCount(data), digest(chunk), base64.RawStdEncoding.EncodeToString(chunk)), nil
}

func parseFrame(text string) (string, int, int, []byte, error) {
	if len(text) > 250000 || len(text) == 0 {
		return "", 0, 0, nil, errors.New("part size")
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 {
		return "", 0, 0, nil, errors.New("part lines")
	}
	fields := strings.Split(strings.TrimSuffix(lines[0], "\r"), " ")
	if len(fields) != 5 || fields[0] != "FIGMOBIL/1" || !hexID(fields[1], 64) || !hexID(fields[4], 64) {
		return "", 0, 0, nil, errors.New("part header")
	}
	index, e1 := strconv.Atoi(fields[2])
	total, e2 := strconv.Atoi(fields[3])
	if e1 != nil || e2 != nil || index < 1 || total < 1 || index > total || total > maxParts || strconv.Itoa(index) != fields[2] || strconv.Itoa(total) != fields[3] {
		return "", 0, 0, nil, errors.New("part numbers")
	}
	chunk, err := base64.RawStdEncoding.Strict().DecodeString(strings.TrimSuffix(lines[1], "\r"))
	if err != nil || len(chunk) == 0 || len(chunk) > rawPartBytes || digest(chunk) != fields[4] || (index < total && len(chunk) != rawPartBytes) {
		return "", 0, 0, nil, errors.New("part integrity")
	}
	return fields[1], index, total, chunk, nil
}

func (a *assembly) add(text string) ([]byte, error) {
	id, index, total, chunk, err := parseFrame(text)
	if err != nil {
		return nil, err
	}
	if a.ID != "" && (a.ID != id || a.Total != total) {
		return nil, errors.New("mixed messages")
	}
	if a.ID == "" {
		a.ID = id
		a.Total = total
		a.Chunks = map[int][]byte{}
	}
	if previous, ok := a.Chunks[index]; ok {
		if !bytes.Equal(previous, chunk) {
			return nil, errors.New("different duplicate")
		}
	} else {
		if a.Bytes+len(chunk) > MaxMessageBytes {
			return nil, errors.New("message size")
		}
		a.Chunks[index] = chunk
		a.Bytes += len(chunk)
	}
	if len(a.Chunks) != a.Total {
		return nil, nil
	}
	data := make([]byte, 0, a.Bytes)
	for i := 1; i <= a.Total; i++ {
		data = append(data, a.Chunks[i]...)
	}
	if digest(data) != a.ID {
		return nil, errors.New("message integrity")
	}
	return data, nil
}
