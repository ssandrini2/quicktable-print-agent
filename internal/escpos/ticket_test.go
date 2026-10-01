package escpos

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func intPtr(n int) *int { return &n }

// printed is the ticket's text as the paper shows it: commands stripped,
// PC858 bytes kept as they are.
func printed(data []byte) []string {
	var text []byte
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case esc:
			if data[i+1] == '@' {
				i++
			} else {
				i += 2
			}
		case gs:
			if data[i+1] == 'V' {
				i += 3
			} else {
				i += 2
			}
		default:
			text = append(text, data[i])
		}
	}
	return strings.Split(strings.TrimRight(string(text), "\n"), "\n")
}

func order() Ticket {
	return Ticket{
		Kind:        "order",
		Title:       "Cocina",
		TableNumber: intPtr(5),
		OrderNumber: intPtr(42),
		People:      intPtr(2),
		PlacedAt:    "2026-10-01T23:15:00.000Z",
		Note:        "Apurados",
		Lines: []Line{
			{
				Quantity:    2,
				Name:        "Bife de chorizo",
				SubProducts: []SubProduct{{Name: "Papas fritas", Quantity: 1}, {Name: "Huevo", Quantity: 2}},
				MeatPoint:   "MEDIUM_RARE",
				ServingTime: "WITH_MAIN",
				Note:        "sin sal",
			},
			{Quantity: 1, Name: "Ensalada"},
		},
	}
}

func TestRenderOrder(t *testing.T) {
	data := Render(order(), Options{PaperWidthMm: 58, Location: time.FixedZone("ART", -3*3600)})

	want := []string{
		"COCINA",
		strings.Repeat("-", 32),
		"Mesa 5",
		"Pedido #42 - 2 pers.       20:15",
		strings.Repeat("-", 32),
		"2 x Bife de chorizo",
		"  + Papas fritas",
		"  + 2 x Huevo",
		"  Punto: A punto jugoso",
		"  Con el principal",
		"  >> sin sal",
		"1 x Ensalada",
		strings.Repeat("-", 32),
		"NOTA: Apurados",
	}
	if got := printed(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("printed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !bytes.HasPrefix(data, []byte{esc, '@', esc, 't', CodePagePC858}) {
		t.Error("the ticket must start by resetting the printer and selecting the code page")
	}
	if !bytes.HasSuffix(data, []byte{gs, 'V', 66, 0}) {
		t.Error("the ticket must end with a cut")
	}
}

func TestRenderMarksReprintsAndContinuations(t *testing.T) {
	ticket := order()
	ticket.Continuation = true

	lines := printed(Render(ticket, Options{PaperWidthMm: 80, Reprint: true}))

	if lines[1] != string(encode("*** REIMPRESIÓN ***")) || lines[2] != string(encode("(continuación)")) {
		t.Fatalf("got %q", lines[:3])
	}
	if len(lines[3]) != 48 {
		t.Errorf("an 80 mm rule is 48 characters, got %d", len(lines[3]))
	}
}

func TestRenderNeverExceedsThePaper(t *testing.T) {
	ticket := order()
	ticket.Lines[0].Name = "Milanesa napolitana a caballo con guarnición de papas rústicas y ensalada"
	ticket.Lines[0].Note = "Supercalifragilisticoespialidosoextralargoparaunalinea de papel"
	ticket.Note = strings.Repeat("muy apurados ", 8)

	for _, width := range []int{58, 80} {
		for _, line := range printed(Render(ticket, Options{PaperWidthMm: width})) {
			if len(line) > charsPerLine(width) {
				t.Errorf("%d mm: line of %d characters: %q", width, len(line), line)
			}
		}
	}
}

func TestRenderTest(t *testing.T) {
	lines := printed(Render(Ticket{Kind: "test", Title: "Barra", PlacedAt: "2026-10-01T12:00:00Z"}, Options{PaperWidthMm: 80}))

	text := strings.Join(lines, "\n")
	if !strings.Contains(text, string(encode("PRUEBA DE IMPRESIÓN"))) || !strings.Contains(text, "48 caracteres") {
		t.Fatalf("got:\n%s", text)
	}
}

func TestEncode(t *testing.T) {
	got := encode("Ñoquis “al dente” — 5€ 🍝")
	want := []byte{0xA5, 'o', 'q', 'u', 'i', 's', ' ', '"', 'a', 'l', ' ', 'd', 'e', 'n', 't', 'e', '"', ' ', '-', ' ', '5', 0xD5, ' ', '?'}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x\nwant % x", got, want)
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"uno dos tres", 7, []string{"uno dos", "tres"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"ñandú ñandú", 5, []string{"ñandú", "ñandú"}},
		{"", 10, nil},
	}
	for _, c := range cases {
		if got := wrap(c.text, c.width); !reflect.DeepEqual(got, c.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
}
