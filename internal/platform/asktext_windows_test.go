//go:build windows

package platform

import (
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procFindWindow   = user32.NewProc("FindWindowW")
	procFindWindowEx = user32.NewProc("FindWindowExW")
)

const wmSetText = 0x000C

// asks opens the window and hands it to act once it is on screen (for an
// instant: these tests answer it themselves).
func asks(t *testing.T, act func(window, edit uintptr)) (string, bool) {
	t.Helper()
	type answer struct {
		text     string
		accepted bool
	}
	done := make(chan answer, 1)
	go func() {
		text, accepted := AskText("QuickTable test", "Type the code", "OK", "Cancel")
		done <- answer{text, accepted}
	}()

	className, _ := windows.UTF16PtrFromString(askTextClass)
	editClass, _ := windows.UTF16PtrFromString("EDIT")
	deadline := time.Now().Add(5 * time.Second)
	for {
		window, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(className)), 0)
		if window != 0 {
			edit, _, _ := procFindWindowEx.Call(window, 0, uintptr(unsafe.Pointer(editClass)), 0)
			act(window, edit)
			break
		}
		select {
		case result := <-done:
			// No desktop to put a window on (a service session): nothing to test.
			t.Skipf("the window could not be shown (answered %q, %v)", result.text, result.accepted)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the window never showed up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case result := <-done:
		return result.text, result.accepted
	case <-time.After(5 * time.Second):
		t.Fatal("the window never closed")
		return "", false
	}
}

func TestAskTextReturnsWhatWasTyped(t *testing.T) {
	text, accepted := asks(t, func(window, edit uintptr) {
		typed, _ := windows.UTF16PtrFromString("K7MP Q2XD")
		procSendMessage.Call(edit, wmSetText, 0, uintptr(unsafe.Pointer(typed)))
		procSendMessage.Call(window, wmCommand, idOK, 0)
	})

	if !accepted || text != "K7MP Q2XD" {
		t.Fatalf("got %q, %v", text, accepted)
	}
}

func TestAskTextCancelled(t *testing.T) {
	for name, close := range map[string]func(window uintptr){
		"cancel button": func(window uintptr) { procSendMessage.Call(window, wmCommand, idCancel, 0) },
		"closed":        func(window uintptr) { procSendMessage.Call(window, wmClose, 0, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			if text, accepted := asks(t, func(window, _ uintptr) { close(window) }); accepted || text != "" {
				t.Fatalf("got %q, %v", text, accepted)
			}
		})
	}
}
