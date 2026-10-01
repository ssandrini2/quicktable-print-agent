//go:build windows

// Package platform is what the agent needs from the operating system besides
// printing: a notice on screen, starting with the session, running once.
package platform

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue  = "QuickTablePrintAgent"
	mutexName = `Local\QuickTablePrintAgent`
	boxTitle  = "QuickTable - Impresión"
)

// Notify shows a message box and returns when the person closes it.
func Notify(text string) {
	title, _ := windows.UTF16PtrFromString(boxTitle)
	body, _ := windows.UTF16PtrFromString(text)
	const flags = windows.MB_OK | windows.MB_ICONINFORMATION | windows.MB_SETFOREGROUND | windows.MB_TOPMOST
	_, _ = windows.MessageBox(0, body, title, flags)
}

// SingleInstance reports whether this is the only agent running in the
// user's session. The claim lasts as long as the process.
func SingleInstance() (bool, error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return false, err
	}
	_, err = windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return false, nil
	}
	return err == nil, err
}

// SetAutostart makes Windows start exePath whenever this user signs in. It is
// a per-user setting: no administrator rights involved.
func SetAutostart(exePath string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(runValue, fmt.Sprintf(`"%s"`, exePath))
}

// RemoveAutostart undoes SetAutostart.
func RemoveAutostart() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
