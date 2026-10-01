//go:build windows

// Package platform is what the agent needs from the operating system besides
// printing: notices on screen, starting with the session, running once, being
// listed among the installed apps.
package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\QuickTablePrintAgent`
	appID        = "QuickTablePrintAgent"
	mutexName    = `Local\QuickTablePrintAgent`
	stopEvent    = `Local\QuickTablePrintAgentStop`

	// LANG_ENGLISH, the primary language of a Windows LANGID.
	langEnglish = 0x09
	// IDYES, what MessageBox returns for the Yes button.
	idYes = 6
)

var procGetUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

func messageBox(title, text string, flags uint32) int32 {
	caption, _ := windows.UTF16PtrFromString(title)
	body, _ := windows.UTF16PtrFromString(text)
	result, _ := windows.MessageBox(0, body, caption, flags|windows.MB_SETFOREGROUND|windows.MB_TOPMOST)
	return result
}

// Notify shows a message box and returns when the person closes it.
func Notify(title, text string) {
	messageBox(title, text, windows.MB_OK|windows.MB_ICONINFORMATION)
}

// Confirm asks a yes/no question; the buttons come in Windows' own language.
func Confirm(title, text string) bool {
	return messageBox(title, text, windows.MB_YESNO|windows.MB_ICONQUESTION) == idYes
}

// EnglishUI reports whether this user's Windows is in English.
func EnglishUI() bool {
	langID, _, _ := procGetUserDefaultUILanguage.Call()
	return langID&0x3ff == langEnglish
}

// SingleInstance claims being the only agent running in the user's session.
// It reports false when another one already is. release gives the claim up
// (it otherwise lasts as long as the process).
func SingleInstance() (only bool, release func(), err error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return false, nil, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return false, func() {}, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, func() { _ = windows.CloseHandle(handle) }, nil
}

// StopRequested returns a channel closed when another process asks this
// agent to stop (RequestStop): an uninstall, or a reinstall over it.
func StopRequested() (<-chan struct{}, error) {
	name, err := windows.UTF16PtrFromString(stopEvent)
	if err != nil {
		return nil, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	// Left over from an agent that was just asked to stop: start clean.
	_ = windows.ResetEvent(event)
	stopped := make(chan struct{})
	go func() {
		if _, err := windows.WaitForSingleObject(event, windows.INFINITE); err == nil {
			close(stopped)
		}
	}()
	return stopped, nil
}

// RequestStop asks the running agent, if there is one, to stop. It reports
// whether there was one to ask.
func RequestStop() bool {
	name, err := windows.UTF16PtrFromString(stopEvent)
	if err != nil {
		return false
	}
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(event)
	return windows.SetEvent(event) == nil
}

// SetAutostart makes Windows start exePath whenever this user signs in. It is
// a per-user setting: no administrator rights involved.
func SetAutostart(exePath string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(appID, fmt.Sprintf(`"%s"`, exePath))
}

// RemoveAutostart undoes SetAutostart.
func RemoveAutostart() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(appID); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// RegisterUninstall lists the agent in Windows' "Installed apps" for this
// user, with its own --uninstall as the way to remove it.
func RegisterUninstall(exePath, installDir, displayName, version string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	values := map[string]string{
		"DisplayName":     displayName,
		"DisplayVersion":  version,
		"Publisher":       "QuickTable",
		"DisplayIcon":     exePath,
		"InstallLocation": installDir,
		"UninstallString": fmt.Sprintf(`"%s" --uninstall`, exePath),
	}
	for name, value := range values {
		if err := key.SetStringValue(name, value); err != nil {
			return err
		}
	}
	for _, name := range []string{"NoModify", "NoRepair"} {
		if err := key.SetDWordValue(name, 1); err != nil {
			return err
		}
	}
	return nil
}

// RemoveUninstall undoes RegisterUninstall.
func RemoveUninstall() error {
	err := registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveDirLater deletes dir a few seconds from now, once this process — whose
// own executable is in it — has exited.
func RemoveDirLater(dir string) error {
	script := fmt.Sprintf(`ping -n 4 127.0.0.1 >nul & rmdir /s /q "%s"`, dir)
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CmdLine:       `cmd.exe /c ` + script,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd.Start()
}
