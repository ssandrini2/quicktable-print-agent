// Package escpos lays a ticket out as ESC/POS bytes for a thermal printer.
package escpos

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Ticket is what the API freezes in a print job: data, not formatting.
type Ticket struct {
	Kind         string `json:"kind"` // "order", "test" or "bill"
	Title        string `json:"title"`
	TableNumber  *int   `json:"tableNumber"`
	OrderNumber  *int   `json:"orderNumber"`
	People       *int   `json:"people"`
	PlacedAt     string `json:"placedAt"`
	Note         string `json:"note"`
	Continuation bool   `json:"continuation"`
	Lines        []Line `json:"lines"`
	// Bill is set on a guest's bill (kind "bill"); its Lines stay empty.
	Bill *Bill `json:"bill"`
}

// Bill is what the guests pay, line by line, as the tablet shows it.
type Bill struct {
	Lines     []BillLine     `json:"lines"`
	Discounts []BillDiscount `json:"discounts"`
	Total     float64        `json:"total"`
}

// BillLine is one product on the bill and what it costs in all.
type BillLine struct {
	Quantity int     `json:"quantity"`
	Name     string  `json:"name"`
	Amount   float64 `json:"amount"`
}

// BillDiscount is one discount off the bill.
type BillDiscount struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

// Line is one product of the order.
type Line struct {
	Quantity    int          `json:"quantity"`
	Name        string       `json:"name"`
	SubProducts []SubProduct `json:"subProducts"`
	MeatPoint   string       `json:"meatPoint"`
	ServingTime string       `json:"servingTime"`
	Note        string       `json:"note"`
}

// SubProduct is an add-on or variant picked for a line.
type SubProduct struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

// Options is how one job prints.
type Options struct {
	// PaperWidthMm is 58 or 80.
	PaperWidthMm int
	// Reprint marks the ticket so the kitchen can tell a duplicate.
	Reprint bool
	// Location is the time zone the order's time prints in (the PC's by default).
	Location *time.Location
}

var meatPoints = map[string]string{
	"RARE":        "Jugoso",
	"MEDIUM_RARE": "A punto jugoso",
	"MEDIUM":      "A punto",
	"MEDIUM_WELL": "A punto cocido",
	"WELL_DONE":   "Cocido",
}

var servingTimes = map[string]string{
	"ENTRY":     "De entrada",
	"WITH_MAIN": "Con el principal",
}

// charsPerLine is how many characters of the standard font fit the paper.
func charsPerLine(paperWidthMm int) int {
	if paperWidthMm == 58 {
		return 32
	}
	return 48
}

// Render lays the ticket out, ending with a paper cut.
func Render(ticket Ticket, options Options) []byte {
	cols := charsPerLine(options.PaperWidthMm)
	p := newPage(cols)

	p.align(center)
	p.size(doubleWidth | doubleHeight)
	p.bold(true)
	for _, line := range wrap(strings.ToUpper(ticket.Title), cols/2) {
		p.text(line)
	}
	p.size(normal)
	if options.Reprint {
		p.text("*** REIMPRESIÓN ***")
	}
	if ticket.Continuation {
		p.text("(continuación)")
	}
	p.bold(false)
	p.align(left)

	switch {
	case ticket.Kind == "test":
		renderTest(p, ticket, options)
	case ticket.Kind == "bill" && ticket.Bill != nil:
		renderBill(p, ticket, options)
	default:
		renderOrder(p, ticket, options)
	}

	p.feed(4)
	p.cut()
	return p.bytes()
}

func renderOrder(p *page, ticket Ticket, options Options) {
	p.rule()
	if ticket.TableNumber != nil {
		p.bold(true)
		p.size(doubleWidth | doubleHeight)
		p.text(fmt.Sprintf("Mesa %d", *ticket.TableNumber))
		p.size(normal)
		p.bold(false)
	}
	var details []string
	if ticket.OrderNumber != nil {
		details = append(details, fmt.Sprintf("Pedido #%d", *ticket.OrderNumber))
	}
	if ticket.People != nil && *ticket.People > 0 {
		details = append(details, fmt.Sprintf("%d pers.", *ticket.People))
	}
	p.columns(strings.Join(details, " - "), clock(ticket.PlacedAt, options.Location))
	p.rule()

	for i, line := range ticket.Lines {
		if i > 0 {
			p.feed(1)
		}
		p.bold(true)
		p.size(doubleHeight)
		p.wrapped(fmt.Sprintf("%d x %s", line.Quantity, line.Name), "    ")
		p.size(normal)
		p.bold(false)
		for _, sub := range line.SubProducts {
			if sub.Quantity > 1 {
				p.wrapped(fmt.Sprintf("  + %d x %s", sub.Quantity, sub.Name), "      ")
			} else {
				p.wrapped("  + "+sub.Name, "    ")
			}
		}
		if label := meatPoints[line.MeatPoint]; label != "" {
			p.wrapped("  Punto: "+label, "    ")
		}
		if label := servingTimes[line.ServingTime]; label != "" {
			p.wrapped("  "+label, "    ")
		}
		if note := strings.TrimSpace(line.Note); note != "" {
			p.bold(true)
			p.wrapped("  >> "+note, "     ")
			p.bold(false)
		}
	}
	p.rule()

	if note := strings.TrimSpace(ticket.Note); note != "" {
		p.bold(true)
		p.wrapped("NOTA: "+note, "")
		p.bold(false)
	}
}

func renderBill(p *page, ticket Ticket, options Options) {
	p.rule()
	if ticket.TableNumber != nil {
		p.bold(true)
		p.size(doubleWidth | doubleHeight)
		p.text(fmt.Sprintf("Mesa %d", *ticket.TableNumber))
		p.size(normal)
		p.bold(false)
	}
	var details []string
	if ticket.People != nil && *ticket.People > 0 {
		details = append(details, fmt.Sprintf("%d pers.", *ticket.People))
	}
	p.columns(strings.Join(details, " - "), clock(ticket.PlacedAt, options.Location))
	p.rule()

	for _, line := range ticket.Bill.Lines {
		amount := money(line.Amount)
		// The name takes what the amount leaves; a long one goes on to the next lines.
		names := wrapWidths(fmt.Sprintf("%d x %s", line.Quantity, line.Name), p.cols-utf8.RuneCountInString(amount)-1, p.cols-4)
		p.columns(names[0], amount)
		for _, rest := range names[1:] {
			p.text("    " + rest)
		}
	}
	if len(ticket.Bill.Discounts) > 0 {
		p.feed(1)
		for _, discount := range ticket.Bill.Discounts {
			p.columns(discount.Label, "-"+money(discount.Amount))
		}
	}
	p.rule()
	p.bold(true)
	p.size(doubleHeight)
	p.columns("TOTAL", money(ticket.Bill.Total))
	p.size(normal)
	p.bold(false)
	p.rule()
	p.align(center)
	// A bill before payment isn't the invoice (Fudo issues that): it has to say so.
	p.text("Documento no válido como factura")
	p.feed(1)
	p.text("Gracias por tu visita")
	p.align(left)
}

// money is an amount the way it's written in Argentina: $ 12.500, $ 99,50.
func money(amount float64) string {
	cents := int64(amount*100 + 0.5)
	if amount < 0 {
		cents = int64(amount*100 - 0.5)
	}
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	whole := fmt.Sprintf("%d", cents/100)
	var grouped strings.Builder
	for i, digit := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteByte('.')
		}
		grouped.WriteRune(digit)
	}
	text := sign + "$ " + grouped.String()
	if rest := cents % 100; rest != 0 {
		text += fmt.Sprintf(",%02d", rest)
	}
	return text
}

func renderTest(p *page, ticket Ticket, options Options) {
	p.rule()
	p.align(center)
	p.text("PRUEBA DE IMPRESIÓN")
	p.text("QuickTable")
	p.align(left)
	p.rule()
	p.columns("Ancho", fmt.Sprintf("%d caracteres", p.cols))
	p.columns("Hora", clock(ticket.PlacedAt, options.Location))
	p.text("áéíóú ÁÉÍÓÚ ñ Ñ ü ¿? ¡!")
	p.text(strings.Repeat("1234567890", 5)[:p.cols])
	p.rule()
	p.text("Si ves esto, la impresora funciona.")
}

// clock is the order's time of day, in the given zone.
func clock(iso string, location *time.Location) string {
	at, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	if location == nil {
		location = time.Local
	}
	return at.In(location).Format("15:04")
}

// wrap breaks text into lines of at most width characters, at spaces where it can.
func wrap(text string, width int) []string {
	return wrapWidths(text, width, width)
}

// wrapWidths is wrap with a different width for the first line and the rest.
func wrapWidths(text string, first, rest int) []string {
	var lines []string
	width := func() int {
		w := rest
		if len(lines) == 0 {
			w = first
		}
		if w < 1 {
			w = 1
		}
		return w
	}
	current := ""
	flush := func() {
		lines = append(lines, current)
		current = ""
	}
	for _, word := range strings.Fields(text) {
		if current != "" && utf8.RuneCountInString(current)+1+utf8.RuneCountInString(word) <= width() {
			current += " " + word
			continue
		}
		if current != "" {
			flush()
		}
		// A word longer than a line is cut across lines.
		runes := []rune(word)
		for len(runes) > width() {
			w := width()
			current = string(runes[:w])
			runes = runes[w:]
			flush()
		}
		current = string(runes)
	}
	if current != "" {
		flush()
	}
	return lines
}
