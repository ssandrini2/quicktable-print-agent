package escpos

// CodePagePC858 is the ESC t argument that selects PC858 (Latin-1 with the
// euro sign) on Epson and Epson-compatible printers.
const CodePagePC858 = 19

// pc858 maps the non-ASCII characters a Spanish ticket uses to their PC858 byte.
var pc858 = map[rune]byte{
	'Ç': 0x80, 'ü': 0x81, 'é': 0x82, 'â': 0x83, 'ä': 0x84, 'à': 0x85, 'å': 0x86, 'ç': 0x87,
	'ê': 0x88, 'ë': 0x89, 'è': 0x8A, 'ï': 0x8B, 'î': 0x8C, 'ì': 0x8D, 'Ä': 0x8E, 'Å': 0x8F,
	'É': 0x90, 'æ': 0x91, 'Æ': 0x92, 'ô': 0x93, 'ö': 0x94, 'ò': 0x95, 'û': 0x96, 'ù': 0x97,
	'ÿ': 0x98, 'Ö': 0x99, 'Ü': 0x9A, 'ø': 0x9B, '£': 0x9C, 'Ø': 0x9D, '×': 0x9E, 'ƒ': 0x9F,
	'á': 0xA0, 'í': 0xA1, 'ó': 0xA2, 'ú': 0xA3, 'ñ': 0xA4, 'Ñ': 0xA5, 'ª': 0xA6, 'º': 0xA7,
	'¿': 0xA8, '®': 0xA9, '¬': 0xAA, '½': 0xAB, '¼': 0xAC, '¡': 0xAD, '«': 0xAE, '»': 0xAF,
	'Á': 0xB5, 'Â': 0xB6, 'À': 0xB7, '©': 0xB8, '¢': 0xBD, '¥': 0xBE,
	'ã': 0xC6, 'Ã': 0xC7, '¤': 0xCF,
	'ð': 0xD0, 'Ð': 0xD1, 'Ê': 0xD2, 'Ë': 0xD3, 'È': 0xD4, '€': 0xD5, 'Í': 0xD6, 'Î': 0xD7,
	'Ï': 0xD8, 'Ì': 0xDE,
	'Ó': 0xE0, 'ß': 0xE1, 'Ô': 0xE2, 'Ò': 0xE3, 'õ': 0xE4, 'Õ': 0xE5, 'µ': 0xE6, 'þ': 0xE7,
	'Þ': 0xE8, 'Ú': 0xE9, 'Û': 0xEA, 'Ù': 0xEB, 'ý': 0xEC, 'Ý': 0xED, '¯': 0xEE, '´': 0xEF,
	'±': 0xF1, '¾': 0xF3, '¶': 0xF4, '§': 0xF5, '÷': 0xF6, '¸': 0xF7, '°': 0xF8, '¨': 0xF9,
	'·': 0xFA, '¹': 0xFB, '³': 0xFC, '²': 0xFD,
}

// lookalikes replaces typographic characters PC858 lacks with plain ones.
var lookalikes = map[rune]string{
	'‘': "'", '’': "'", '“': `"`, '”': `"`, '–': "-", '—': "-", '…': "...", '•': "*", ' ': " ",
}

// encode turns text into PC858 bytes. A character the code page lacks prints
// as "?" — a ticket never fails over a character.
func encode(text string) []byte {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		switch {
		case r >= 0x20 && r < 0x7F:
			out = append(out, byte(r))
		case lookalikes[r] != "":
			out = append(out, lookalikes[r]...)
		default:
			if b, ok := pc858[r]; ok {
				out = append(out, b)
			} else if r >= 0x20 {
				out = append(out, '?')
			}
		}
	}
	return out
}
