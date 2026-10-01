package escpos

import "strings"

// unicodeOf is pc858 reversed: byte → character.
var unicodeOf = func() map[byte]rune {
	reversed := make(map[byte]rune, len(pc858))
	for r, b := range pc858 {
		reversed[b] = r
	}
	return reversed
}()

// Preview turns ESC/POS bytes back into the text a printer would put on
// paper — for the virtual printer and for tests. Formatting is reduced to
// what reads in plain text: big lines are marked, a cut is a dashed line.
func Preview(data []byte) string {
	var out strings.Builder
	var line strings.Builder
	big := false
	centered := false
	width := 48

	flush := func() {
		text := line.String()
		line.Reset()
		if centered && len([]rune(text)) < width {
			text = strings.Repeat(" ", (width-len([]rune(text)))/2) + text
		}
		if big && strings.TrimSpace(text) != "" {
			text = "## " + strings.TrimLeft(text, " ")
		}
		out.WriteString(strings.TrimRight(text, " "))
		out.WriteByte('\n')
	}

	for i := 0; i < len(data); i++ {
		b := data[i]
		arg := func(n int) byte {
			if i+n < len(data) {
				return data[i+n]
			}
			return 0
		}
		switch {
		case b == esc && arg(1) == '@':
			i++
		case b == esc && arg(1) == 'a':
			centered = arg(2) == 1
			i += 2
		case b == esc && arg(1) == 'd':
			if line.Len() > 0 {
				flush()
			}
			out.WriteString(strings.Repeat("\n", int(arg(2))))
			i += 2
		case b == esc:
			i += 2
		case b == gs && arg(1) == '!':
			big = arg(2)&doubleWidth != 0
			i += 2
		case b == gs && arg(1) == 'V':
			if line.Len() > 0 {
				flush()
			}
			out.WriteString("- - - - - - - - - - - - cut - - - - - - - - - - - -\n")
			i += 3
		case b == gs:
			i += 2
		case b == '\n':
			flush()
		case b >= 0x20 && b < 0x7F:
			line.WriteByte(b)
		default:
			if r, ok := unicodeOf[b]; ok {
				line.WriteRune(r)
			} else if b >= 0x80 {
				line.WriteByte('?')
			}
		}
	}
	if line.Len() > 0 {
		flush()
	}
	return out.String()
}
