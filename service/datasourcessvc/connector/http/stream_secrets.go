package http

import (
	"io"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Match both raw credential bytes and JSON string escapes, including across
// chunks. Inspecting quoted strings conservatively also covers non-JSON files.
type secretPattern struct {
	value        []byte
	prefix       []int
	raw, decoded int
}

func newSecretPattern(value string) secretPattern {
	pattern := secretPattern{value: []byte(value), prefix: make([]int, len(value))}
	for i, j := 1, 0; i < len(value); i++ {
		for j > 0 && value[i] != value[j] {
			j = pattern.prefix[j-1]
		}
		if value[i] == value[j] {
			j++
		}
		pattern.prefix[i] = j
	}
	return pattern
}

func (p *secretPattern) feed(c byte, decoded bool) bool {
	position := &p.raw
	if decoded {
		position = &p.decoded
	}
	if *position == len(p.value) {
		return true
	}
	for *position > 0 && c != p.value[*position] {
		*position = p.prefix[*position-1]
	}
	if c == p.value[*position] {
		*position++
	}
	return *position == len(p.value)
}

type secretStreamWriter struct {
	dst              io.Writer
	patterns         []secretPattern
	quoted, escape   bool
	unicodeRemaining int
	code             uint16
	high             rune
	found            bool
}

func newSecretStreamWriter(dst io.Writer, secrets []string) *secretStreamWriter {
	writer := &secretStreamWriter{dst: dst}
	for _, value := range secrets {
		if value != "" {
			writer.patterns = append(writer.patterns, newSecretPattern(value))
		}
	}
	return writer
}

func (w *secretStreamWriter) decoded(c byte) {
	for i := range w.patterns {
		if w.patterns[i].feed(c, true) {
			w.found = true
		}
	}
}

func (w *secretStreamWriter) rune(value rune) {
	var scratch [utf8.UTFMax]byte
	for _, c := range utf8.AppendRune(scratch[:0], value) {
		w.decoded(c)
	}
}

func (w *secretStreamWriter) flushHigh() {
	if w.high != 0 {
		w.rune(utf8.RuneError)
		w.high = 0
	}
}

func (w *secretStreamWriter) endString() {
	w.flushHigh()
	w.quoted = false
	w.escape = false
	w.unicodeRemaining = 0
	for i := range w.patterns {
		w.patterns[i].decoded = 0
	}
}

func (w *secretStreamWriter) scan(c byte) {
	if w.unicodeRemaining > 0 {
		var digit byte
		switch {
		case c >= '0' && c <= '9':
			digit = c - '0'
		case c >= 'a' && c <= 'f':
			digit = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			digit = c - 'A' + 10
		default:
			w.endString()
			return
		}
		w.code = w.code<<4 | uint16(digit)
		w.unicodeRemaining--
		if w.unicodeRemaining == 0 {
			value := rune(w.code)
			if w.high != 0 && value >= 0xdc00 && value <= 0xdfff {
				w.rune(utf16.DecodeRune(w.high, value))
				w.high = 0
				return
			}
			w.flushHigh()
			switch {
			case value >= 0xd800 && value <= 0xdbff:
				w.high = value
			case value >= 0xdc00 && value <= 0xdfff:
				w.rune(utf8.RuneError)
			default:
				w.rune(value)
			}
		}
		return
	}
	if w.escape {
		w.escape = false
		if c == 'u' {
			w.code = 0
			w.unicodeRemaining = 4
			return
		}
		w.flushHigh()
		switch c {
		case '"', '\\', '/':
			w.decoded(c)
		case 'b':
			w.decoded('\b')
		case 'f':
			w.decoded('\f')
		case 'n':
			w.decoded('\n')
		case 'r':
			w.decoded('\r')
		case 't':
			w.decoded('\t')
		default:
			w.endString()
		}
		return
	}
	if !w.quoted {
		if c == '"' {
			w.quoted = true
		}
		return
	}
	switch c {
	case '"':
		w.endString()
	case '\\':
		w.escape = true
	default:
		w.flushHigh()
		w.decoded(c)
	}
}

func (w *secretStreamWriter) Write(p []byte) (int, error) {
	if w.found {
		return 0, xerror.HTTPAcknowledgementUncertain()
	}
	for _, c := range p {
		for i := range w.patterns {
			if w.patterns[i].feed(c, false) {
				w.found = true
			}
		}
		w.scan(c)
		if w.found {
			return 0, xerror.HTTPAcknowledgementUncertain().WithPublicDetail("HTTP response contained credential material and was withheld. Verify remote state before retrying a write.")
		}
	}
	n, err := w.dst.Write(p)
	if err != nil {
		return n, xerror.Unexpected().With(err)
	}
	return n, nil
}
