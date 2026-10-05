//go:build !windows

// Package tray is the agent's icon in the Windows notification area. Outside
// Windows (development) there is none: the work just runs.
package tray

// Options is what the icon's menu says and does.
type Options struct {
	Title        string
	ConnectLabel string
	OnConnect    func()
	ExitLabel    string
	OnExit       func()
}

// Tray is the icon once it is on screen.
type Tray struct{}

// Run calls work and returns once it is done.
func Run(_ Options, work func(*Tray)) { work(&Tray{}) }

// SetStatus does nothing.
func (t *Tray) SetStatus(string) {}

// OfferConnect does nothing.
func (t *Tray) OfferConnect(bool) {}
