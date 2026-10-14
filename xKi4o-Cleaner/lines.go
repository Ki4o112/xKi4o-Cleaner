package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// lineSplitter turns console output into lines for the UI. A line that
// ends in a bare carriage return is a progress line (DISM, SFC redraw it
// in place), so it is sent with replace=true and the UI overwrites it.
type lineSplitter struct {
	onLine    func(text string, replace bool)
	cur       []rune
	sawCR     bool
	prevCR    bool
	lastEmpty bool
}

func (s *lineSplitter) feed(text string) {
	for _, r := range text {
		if s.sawCR {
			s.sawCR = false
			if r == '\n' {
				s.endLine()
				continue
			}
			s.endProgress()
		}
		switch r {
		case '\r':
			s.sawCR = true
		case '\n':
			s.endLine()
		case '\x00', '\b':
		default:
			s.cur = append(s.cur, r)
		}
	}
}

func (s *lineSplitter) flush() {
	if s.sawCR {
		s.sawCR = false
		s.endLine()
		return
	}
	if len(s.cur) > 0 {
		s.endLine()
	}
}

func (s *lineSplitter) endLine() {
	s.emit(s.prevCR)
	s.prevCR = false
}

func (s *lineSplitter) endProgress() {
	if strings.TrimSpace(string(s.cur)) == "" {
		s.cur = s.cur[:0]
		return
	}
	s.emit(true)
	s.prevCR = true
}

func (s *lineSplitter) emit(replace bool) {
	text := strings.TrimRight(string(s.cur), " \t")
	s.cur = s.cur[:0]
	empty := strings.TrimSpace(text) == ""
	if empty && (s.lastEmpty || replace) {
		return
	}
	s.lastEmpty = empty
	if s.onLine != nil {
		s.onLine(text, replace)
	}
}

var rePercent = regexp.MustCompile(`(\d{1,3}(?:[.,]\d+)?)\s?%`)

// percentIn returns the last percentage found in a line, or -1.
func percentIn(line string) float64 {
	m := rePercent.FindAllStringSubmatch(line, -1)
	if len(m) == 0 {
		return -1
	}
	v, err := strconv.ParseFloat(strings.Replace(m[len(m)-1][1], ",", ".", 1), 64)
	if err != nil || v < 0 || v > 100 {
		return -1
	}
	return v
}

// utf16Decoder decodes little-endian UTF-16 arriving in arbitrary chunks.
type utf16Decoder struct{ odd []byte }

func (d *utf16Decoder) decode(b []byte) string {
	if len(d.odd) > 0 {
		b = append(d.odd, b...)
		d.odd = nil
	}
	if len(b)%2 == 1 {
		d.odd = []byte{b[len(b)-1]}
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	if len(u) > 0 && u[0] == 0xFEFF {
		u = u[1:]
	}
	return string(utf16.Decode(u))
}

// looksUTF16 guesses whether console output is UTF-16LE (sfc.exe writes
// UTF-16 when its output is redirected).
func looksUTF16(b []byte) bool {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return true
	}
	n, zeros := 0, 0
	for i := 1; i < len(b) && i < 64; i += 2 {
		n++
		if b[i] == 0 {
			zeros++
		}
	}
	return n >= 2 && zeros*10 >= n*7
}
