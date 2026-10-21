package compare

import (
	"bytes"
	"unicode/utf8"
)

// Representation is descriptive only. No normalization, re-encoding, or text
// content leaves this function. Unknown encodings remain opaque byte sequences.
func analyzeRepresentation(data []byte, m *Metadata) {
	m.Encoding = "utf-8"
	m.BOM = "none"
	m.EOL = "none"
	var units []uint16
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe, 0, 0}), bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff}):
		m.Encoding = "unknown"
		m.BOM = "utf-32"
		return
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		little := data[0] == 0xff
		m.Encoding = "utf-16be"
		if little {
			m.Encoding = "utf-16le"
		}
		m.BOM = m.Encoding
		data = data[2:]
		if len(data)%2 != 0 {
			m.Encoding = "unknown"
			return
		}
		for i := 0; i < len(data); i += 2 {
			v := uint16(data[i])<<8 | uint16(data[i+1])
			if little {
				v = uint16(data[i+1])<<8 | uint16(data[i])
			}
			units = append(units, v)
		}
		for i := 0; i < len(units); i++ {
			v := units[i]
			if v >= 0xd800 && v <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					m.Encoding = "unknown"
					return
				}
				i++
			} else if v >= 0xdc00 && v <= 0xdfff {
				m.Encoding = "unknown"
				return
			}
		}
	default:
		if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
			m.BOM = "utf-8"
			data = data[3:]
		}
		if !utf8.Valid(data) {
			m.Encoding = "unknown"
			return
		}
		for _, b := range data {
			if b == 0 || (b < 32 && b != '\t' && b != '\r' && b != '\n' && b != '\f') {
				m.Encoding = "binary"
				return
			}
		}
		for _, b := range data {
			units = append(units, uint16(b))
		}
	}
	lf, crlf, cr := false, false, false
	for i := 0; i < len(units); i++ {
		switch units[i] {
		case '\r':
			if i+1 < len(units) && units[i+1] == '\n' {
				crlf = true
				i++
			} else {
				cr = true
			}
		case '\n':
			lf = true
		}
	}
	count := 0
	for _, present := range []bool{lf, crlf, cr} {
		if present {
			count++
		}
	}
	if count > 1 {
		m.EOL = "mixed"
	} else if lf {
		m.EOL = "lf"
	} else if crlf {
		m.EOL = "crlf"
	} else if cr {
		m.EOL = "cr"
	}
	if len(units) > 0 {
		m.FinalNewline = units[len(units)-1] == '\n' || units[len(units)-1] == '\r'
	}
}
