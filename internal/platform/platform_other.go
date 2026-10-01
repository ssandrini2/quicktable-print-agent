//go:build !windows

// Package platform is what the agent needs from the operating system besides
// printing: a notice on screen, starting with the session, running once.
// Outside Windows (development) these are stand-ins.
package platform

import "fmt"

// Notify prints the message.
func Notify(text string) { fmt.Println(text) }

// SingleInstance always reports true.
func SingleInstance() (bool, error) { return true, nil }

// SetAutostart does nothing.
func SetAutostart(string) error { return nil }

// RemoveAutostart does nothing.
func RemoveAutostart() error { return nil }
