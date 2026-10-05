//go:build windows

// Package tray is the agent's icon in the Windows notification area: what
// shows it is running, its version and state, and the way to close it.
package tray

import (
	_ "embed"

	"fyne.io/systray"
)

//go:embed icon.ico
var icon []byte

// Options is what the icon's menu says and does.
type Options struct {
	// Title is the menu's first line: the program and its version.
	Title string
	// ConnectLabel and OnConnect: the entry to connect with a code, shown only while unpaired.
	ConnectLabel string
	OnConnect    func()
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
		systray.SetIcon(icon)
		systray.SetTooltip(options.Title)
		title := systray.AddMenuItem(options.Title, "")
		title.Disable()
		status := systray.AddMenuItem("", "")
		status.Disable()
		status.Hide()
		systray.AddSeparator()
		connect := systray.AddMenuItem(options.ConnectLabel, "")
		connect.Hide()
		exit := systray.AddMenuItem(options.ExitLabel, "")

		go func() {
			for {
				select {
				case <-connect.ClickedCh:
					options.OnConnect()
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

// SetStatus shows the agent's state, in the menu and when pointing at the icon.
func (t *Tray) SetStatus(text string) {
	t.status.SetTitle(text)
	t.status.Show()
	systray.SetTooltip(t.title + " - " + text)
}

// OfferConnect shows or hides the entry to connect with a code.
func (t *Tray) OfferConnect(show bool) {
	if show {
		t.connect.Show()
	} else {
		t.connect.Hide()
	}
}
