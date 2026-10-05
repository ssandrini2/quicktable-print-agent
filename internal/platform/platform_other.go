//go:build !windows

// Package platform is what the agent needs from the operating system besides
// printing: notices on screen, starting with the session, running once, being
// listed among the installed apps. Outside Windows (development) these are
// stand-ins.
package platform

import "fmt"

// Notify prints the message.
func Notify(title, text string) { fmt.Printf("[%s] %s\n", title, text) }

// Confirm always answers yes.
func Confirm(title, text string) bool {
	Notify(title, text)
	return true
}

// EnglishUI always reports false.
func EnglishUI() bool { return false }

// SingleInstance always reports true.
func SingleInstance() (bool, func(), error) { return true, func() {}, nil }

// StopRequested never fires.
func StopRequested() (<-chan struct{}, error) { return make(chan struct{}), nil }

// RequestStop finds nobody to ask.
func RequestStop() bool { return false }

// SetAutostart does nothing.
func SetAutostart(string) error { return nil }

// RemoveAutostart does nothing.
func RemoveAutostart() error { return nil }

// RegisterUninstall does nothing.
func RegisterUninstall(string, string, string, string) error { return nil }

// RemoveUninstall does nothing.
func RemoveUninstall() error { return nil }

// RemoveDirLater does nothing.
func RemoveDirLater(string) error { return nil }

// AskText finds nobody to ask.
func AskText(string, string, string, string) (string, bool) { return "", false }
