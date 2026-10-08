package tray

// Kind is what the icon looks like for a state.
type Kind int

const (
	// OK: connected and printing.
	OK Kind = iota
	// Offline: the API can't be reached.
	Offline
	// Unpaired: this PC isn't connected to a restaurant.
	Unpaired
)
