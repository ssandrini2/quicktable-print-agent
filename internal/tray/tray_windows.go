//go:build windows

// Package tray is the agent's icon in the Windows notification area: what
// shows it is running, its version and state, and the way to close it.
package tray

import (
	_ "embed"

	"fyne.io/systray"
)

// The icon itself tells how the agent is doing: plain when all is well, with
// a red dot without connection, grey with an amber dot when this PC isn't
// connected to a restaurant (see scripts/status-icons.py).
var (
	//go:embed icon.ico
	iconOK []byte
	//go:embed icon-offline.ico
	iconOffline []byte
	//go:embed icon-unpaired.ico
	iconUnpaired []byte
)

// Options is what the icon's menu says and does.
type Options struct {
	// Title is the menu's first line: the program and its version.
	Title string
	// ConnectLabel and OnConnect: the entry to connect the PC to a restaurant, shown only while unpaired.
	ConnectLabel string
	OnConnect    func()
	// DiagnosticsLabel and OnDiagnostics: the entry to send the agent's log to QuickTable.
	DiagnosticsLabel string
	OnDiagnostics    func()
	// ExitLabel and OnExit: the entry to close the program (OnExit asks first).
	ExitLabel string
	OnExit    func()
}

// Tray is the icon once it is on screen.
type Tray struct {
	title   string
	status  *systray.MenuItem
	connect *systray.MenuItem
}

// Run shows the icon and calls work with it; it returns once work is done.
// It must be called from the main goroutine.
func Run(options Options, work func(*Tray)) {
	systray.Run(func() {
		systray.SetIcon(iconOK)
		systray.SetTooltip(options.Title)
		title := systray.AddMenuItem(options.Title, "")
		title.Disable()
		status := systray.AddMenuItem("", "")
		status.Disable()
		status.Hide()
		systray.AddSeparator()
		connect := systray.AddMenuItem(options.ConnectLabel, "")
		connect.Hide()
		diagnostics := systray.AddMenuItem(options.DiagnosticsLabel, "")
		systray.AddSeparator()
		exit := systray.AddMenuItem(options.ExitLabel, "")

		go func() {
			for {
				select {
				case <-connect.ClickedCh:
					options.OnConnect()
				case <-diagnostics.ClickedCh:
					// On its own: it waits on the network, and the menu must keep answering.
					go options.OnDiagnostics()
				case <-exit.ClickedCh:
					options.OnExit()
				}
			}
		}()
		go func() {
			work(&Tray{title: options.Title, status: status, connect: connect})
			systray.Quit()
		}()
	}, nil)
}

// SetStatus shows the agent's state: the icon itself, the menu, and the text
// when pointing at the icon.
func (t *Tray) SetStatus(kind Kind, text string) {
	switch kind {
	case Offline:
		systray.SetIcon(iconOffline)
	case Unpaired:
		systray.SetIcon(iconUnpaired)
	default:
		systray.SetIcon(iconOK)
	}
	t.status.SetTitle(text)
	t.status.Show()
	systray.SetTooltip(t.title + " - " + text)
}

// OfferConnect shows or hides the entry to connect the PC to a restaurant.
func (t *Tray) OfferConnect(show bool) {
	if show {
		t.connect.Show()
	} else {
		t.connect.Hide()
	}
}
