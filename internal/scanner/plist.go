package scanner

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// plistTexts returns every key and every string value of a property
// list, XML or binary. XML strings come in document order, so a key's
// string value follows it; binary strings come in object-table order,
// which promises nothing. Anything that is not a whole property list
// of one of the two formats is an error.
func plistTexts(data []byte) ([]string, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		return binaryPlistStrings(data)
	}
	return xmlPlistStrings(data)
}

func xmlPlistStrings(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	var text []byte
	depth, collecting, isPlist := 0, false, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a readable property list: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local != "plist" {
					return nil, fmt.Errorf("not a property list: root element <%s>", t.Name.Local)
				}
				isPlist = true
			}
			depth++
			collecting = t.Name.Local == "string" || t.Name.Local == "key"
			text = text[:0]
		case xml.CharData:
			if collecting {
				text = append(text, t...)
			}
		case xml.EndElement:
			depth--
			if collecting {
				out = append(out, string(text))
				collecting = false
			}
		}
	}
	if !isPlist {
		return nil, errors.New("not a property list")
	}
	return out, nil
}

// binaryPlistStrings reads the string objects of a bplist00 file
// straight from its object table: the table lists every object, so no
// reference needs following. An object type the format does not
// define is an error, so a string cannot hide behind one.
func binaryPlistStrings(data []byte) ([]string, error) {
	const header, trailer = 8, 32
	bad := func(what string) ([]string, error) {
		return nil, fmt.Errorf("not a readable binary property list: %s", what)
	}
	if len(data) < header+trailer || string(data[:header]) != "bplist00" {
		return bad("unknown version or cut short")
	}
	tr := data[len(data)-trailer:]
	offsetSize := int(tr[6])
	count := binary.BigEndian.Uint64(tr[8:16])
	tableAt := binary.BigEndian.Uint64(tr[24:32])
	body := uint64(len(data) - trailer)
	if offsetSize < 1 || offsetSize > 8 || tableAt < header || tableAt > body || count > (body-tableAt)/uint64(offsetSize) {
		return bad("offset table out of range")
	}
	// uintAt reads a big-endian integer of size bytes at pos; ok is
	// false when it would run past limit.
	uintAt := func(pos, size, limit uint64) (v uint64, ok bool) {
		if size > 8 || pos > limit || limit-pos < size {
			return 0, false
		}
		for _, b := range data[pos : pos+size] {
			v = v<<8 | uint64(b)
		}
		return v, true
	}
	var out []string
	for i := range count {
		at, ok := uintAt(tableAt+i*uint64(offsetSize), uint64(offsetSize), body)
		if !ok || at < header || at >= tableAt {
			return bad("object offset out of range")
		}
		kind, n := data[at]>>4, uint64(data[at]&0x0f)
		switch kind {
		case 0x5, 0x6: // ASCII, UTF-16
		case 0x7, 0x9, 0xe, 0xf:
			return bad("unknown object type")
		default:
			continue
		}
		at++
		if n == 0x0f {
			// A length of 15 or more follows as an integer object.
			if at >= tableAt || data[at]>>4 != 0x1 {
				return bad("string length is not an integer")
			}
			size := uint64(1) << (data[at] & 0x0f)
			if n, ok = uintAt(at+1, size, tableAt); !ok {
				return bad("string length out of range")
			}
			at += 1 + size
		}
		width := uint64(1)
		if kind == 0x6 {
			width = 2
		}
		if n > (tableAt-at)/width {
			return bad("string runs past the object table")
		}
		raw := data[at : at+n*width]
		if kind == 0x5 {
			out = append(out, string(raw))
			continue
		}
		units := make([]uint16, n)
		for j := range units {
			units[j] = binary.BigEndian.Uint16(raw[2*j:])
		}
		out = append(out, string(utf16.Decode(units)))
	}
	return out, nil
}
