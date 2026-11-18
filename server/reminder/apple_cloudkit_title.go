package reminder

// Apple CloudKit stores reminder titles and notes as CRDT documents encoded as
// a protobuf payload, gzip-compressed and base64-encoded. This file ports the
// minimal encoder/decoder needed to read list names and write reminder titles.

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

func ckEncodeVarint(v uint64) []byte {
	var buf []byte
	for v > 127 {
		buf = append(buf, byte(v&0x7f)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

func ckEncodeField(fieldNum int, wireType int, data interface{}) []byte {
	tag := byte((fieldNum << 3) | wireType)
	switch wireType {
	case 0:
		v := data.(uint64)
		return append([]byte{tag}, ckEncodeVarint(v)...)
	case 2:
		b := data.([]byte)
		hdr := append([]byte{tag}, ckEncodeVarint(uint64(len(b)))...)
		return append(hdr, b...)
	}
	return nil
}

func ckPosition(replica uint64, offset int64) []byte {
	replicaField := ckEncodeField(1, 0, replica)
	if offset == -1 {
		sentinelTag := byte((2 << 3) | 0)
		offsetField := append([]byte{sentinelTag}, ckEncodeVarint(0xFFFFFFFF)...)
		return append(replicaField, offsetField...)
	}
	return append(replicaField, ckEncodeField(2, 0, uint64(offset))...)
}

// ckEncodeTitle encodes a title string as Apple's CRDT TitleDocument protobuf,
// gzip-compressed and base64-encoded.
func ckEncodeTitle(title string) (string, error) {
	titleBytes := []byte(title)
	charLen := uint64(utf8.RuneCountInString(title))
	docUUID := ckGenerateUUID()

	op1 := make([]byte, 0, 64)
	op1 = append(op1, ckEncodeField(1, 2, ckPosition(0, 0))...)
	op1 = append(op1, ckEncodeField(2, 0, uint64(0))...)
	op1 = append(op1, ckEncodeField(3, 2, ckPosition(0, 0))...)
	op1 = append(op1, ckEncodeField(5, 0, uint64(1))...)

	op2 := make([]byte, 0, 64)
	op2 = append(op2, ckEncodeField(1, 2, ckPosition(1, 0))...)
	op2 = append(op2, ckEncodeField(2, 0, charLen)...)
	op2 = append(op2, ckEncodeField(3, 2, ckPosition(1, 0))...)
	op2 = append(op2, ckEncodeField(5, 0, uint64(2))...)

	op3 := make([]byte, 0, 64)
	op3 = append(op3, ckEncodeField(1, 2, ckPosition(0, -1))...)
	op3 = append(op3, ckEncodeField(2, 0, uint64(0))...)
	op3 = append(op3, ckEncodeField(3, 2, ckPosition(0, -1))...)

	clockPayload := ckEncodeField(1, 0, charLen)
	replicaPayload := ckEncodeField(1, 0, uint64(1))
	uuidEntry := make([]byte, 0, 64)
	uuidEntry = append(uuidEntry, ckEncodeField(1, 2, docUUID)...)
	uuidEntry = append(uuidEntry, ckEncodeField(2, 2, clockPayload)...)
	uuidEntry = append(uuidEntry, ckEncodeField(2, 2, replicaPayload)...)
	metadata := ckEncodeField(1, 2, uuidEntry)
	attrRun := ckEncodeField(1, 0, charLen)

	note := make([]byte, 0, 256)
	note = append(note, ckEncodeField(2, 2, titleBytes)...)
	note = append(note, ckEncodeField(3, 2, op1)...)
	note = append(note, ckEncodeField(3, 2, op2)...)
	note = append(note, ckEncodeField(3, 2, op3)...)
	note = append(note, ckEncodeField(4, 2, metadata)...)
	note = append(note, ckEncodeField(5, 2, attrRun)...)

	document := make([]byte, 0, 256)
	document = append(document, ckEncodeField(1, 0, uint64(0))...)
	document = append(document, ckEncodeField(2, 0, uint64(0))...)
	document = append(document, ckEncodeField(3, 2, note)...)

	outer := make([]byte, 0, 256)
	outer = append(outer, ckEncodeField(1, 0, uint64(0))...)
	outer = append(outer, ckEncodeField(2, 2, document)...)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(outer); err != nil {
		return "", fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("gzip close: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

var ckPrintableRe = regexp.MustCompile(`[^\x{00}-\x{1f}\x{7f}-\x{9f}]{2,}`)

// ckExtractTitle best-effort decodes a CRDT TitleDocument for list names.
func ckExtractTitle(tdB64 string) string {
	if tdB64 == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(tdB64)
	if err != nil {
		return ""
	}
	var text string
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		r, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return ""
		}
		dec, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return ""
		}
		text = string(dec)
	} else {
		text = strings.TrimSpace(string(raw))
		if text != "" {
			return text
		}
		return ""
	}
	for _, s := range ckPrintableRe.FindAllString(text, -1) {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) >= 2 {
			return s
		}
	}
	return ""
}

func ckGenerateUUID() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		now := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			b[i] = byte(now >> (i % 8 * 8))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return b
}

func ckNewUUIDString() string {
	b := ckGenerateUUID()
	return fmt.Sprintf("%08X-%04X-%04X-%04X-%012X",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
