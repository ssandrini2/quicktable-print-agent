package escpos

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const (
	esc = 0x1B
	gs  = 0x1D
)

type alignment byte

const (
	left   alignment = 0
	center alignment = 1
)

// Character size bits for GS !.
const (
	normal       byte = 0x00
	doubleHeight byte = 0x01
	doubleWidth  byte = 0x10
)

// page accumulates the commands of one ticket.
type page struct {
	buf  bytes.Buffer
	cols int
}

func newPage(cols int) *page {
	p := &page{cols: cols}
	p.buf.Write([]byte{esc, '@'})                // reset
	p.buf.Write([]byte{esc, 't', CodePagePC858}) // code page
	return p
}

func (p *page) bytes() []byte { return p.buf.Bytes() }

func (p *page) align(a alignment) { p.buf.Write([]byte{esc, 'a', byte(a)}) }

func (p *page) size(bits byte) { p.buf.Write([]byte{gs, '!', bits}) }

func (p *page) bold(on bool) {
	var n byte
	if on {
		n = 1
	}
	p.buf.Write([]byte{esc, 'E', n})
}

// text prints one line as given (the caller keeps it within the width).
func (p *page) text(line string) {
	p.buf.Write(encode(line))
	p.buf.WriteByte('\n')
}

// wrapped prints text over as many lines as it needs, keeping its leading
// spaces; lines after the first start with indent.
func (p *page) wrapped(text, indent string) {
	leading := text[:len(text)-len(strings.TrimLeft(text, " "))]
	for i, line := range wrapWidths(text, p.cols-len(leading), p.cols-len(indent)) {
		if i == 0 {
			p.text(leading + line)
		} else {
			p.text(indent + line)
		}
	}
}

// columns prints start and end at the two edges of one line.
func (p *page) columns(start, end string) {
	gap := p.cols - utf8.RuneCountInString(start) - utf8.RuneCountInString(end)
	if gap < 1 {
		p.text(start)
		p.text(end)
		return
	}
	p.text(start + strings.Repeat(" ", gap) + end)
}

func (p *page) rule() { p.text(strings.Repeat("-", p.cols)) }

func (p *page) feed(lines byte) { p.buf.Write([]byte{esc, 'd', lines}) }

// cut feeds to the cutter and makes a partial cut (ignored by printers without one).
func (p *page) cut() { p.buf.Write([]byte{gs, 'V', 66, 0}) }
