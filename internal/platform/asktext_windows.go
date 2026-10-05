//go:build windows

package platform

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A small window with a prompt, one text box and OK / Cancel — Windows has
// message boxes but nothing to type into, short of a dialog resource.

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassEx      = user32.NewProc("RegisterClassExW")
	procCreateWindowEx       = user32.NewProc("CreateWindowExW")
	procDefWindowProc        = user32.NewProc("DefWindowProcW")
	procDestroyWindow        = user32.NewProc("DestroyWindow")
	procShowWindow           = user32.NewProc("ShowWindow")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	procSetFocus             = user32.NewProc("SetFocus")
	procGetMessage           = user32.NewProc("GetMessageW")
	procIsDialogMessage      = user32.NewProc("IsDialogMessageW")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procDispatchMessage      = user32.NewProc("DispatchMessageW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procSendMessage          = user32.NewProc("SendMessageW")
	procGetWindowText        = user32.NewProc("GetWindowTextW")
	procGetWindowTextLength  = user32.NewProc("GetWindowTextLengthW")
	procGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	procGetStockObject       = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetStockObject")
	procGetModuleHandle      = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	askTextClass             = "QuickTableAskText"
	askTextRegister          sync.Once
	askTextOne               sync.Mutex // one at a time: the window's state is the package's
	askTextEdit              uintptr
	askTextAnswer            string
	askTextAccepted          bool
	askTextClassRegistration error
)

const (
	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmSetFont = 0x0030
	wmCommand = 0x0111

	idOK      = 1
	idCancel  = 2
	textLimit = 32

	emSetLimitText = 0x00C5

	wsCaptioned     = 0x00C80000 // WS_CAPTION | WS_SYSMENU
	wsExDialogOnTop = 0x00000009 // WS_EX_DLGMODALFRAME | WS_EX_TOPMOST
	wsLabel         = 0x50000000 // WS_CHILD | WS_VISIBLE
	wsEdit          = 0x50810088 // … | WS_TABSTOP | WS_BORDER | ES_UPPERCASE | ES_AUTOHSCROLL
	wsDefaultButton = 0x50010001 // … | WS_TABSTOP | BS_DEFPUSHBUTTON
	wsButton        = 0x50010000 // … | WS_TABSTOP

	colorButtonFace = 15
	defaultGUIFont  = 17
	swShow          = 5
	smScreenWidth   = 0
	smScreenHeight  = 1
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSmall  uintptr
}

type winMessage struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	x, y    int32
}

func askTextProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCommand:
		switch wParam & 0xffff {
		case idOK:
			length, _, _ := procGetWindowTextLength.Call(askTextEdit)
			buffer := make([]uint16, length+1)
			procGetWindowText.Call(askTextEdit, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
			askTextAnswer = windows.UTF16ToString(buffer)
			askTextAccepted = true
			procDestroyWindow.Call(hwnd)
			return 0
		case idCancel:
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProc.Call(hwnd, message, wParam, lParam)
	return result
}

// AskText shows a small window asking the person to type something. It
// returns what they typed (upper-cased) and true, or false if they closed or
// cancelled it.
func AskText(title, prompt, okLabel, cancelLabel string) (string, bool) {
	askTextOne.Lock()
	defer askTextOne.Unlock()
	// A window and its messages belong to the thread that made it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	utf16 := func(text string) uintptr {
		pointer, _ := windows.UTF16PtrFromString(text)
		return uintptr(unsafe.Pointer(pointer))
	}
	instance, _, _ := procGetModuleHandle.Call(0)
	askTextRegister.Do(func() {
		className, _ := windows.UTF16PtrFromString(askTextClass)
		class := wndClassEx{
			wndProc:    windows.NewCallback(askTextProc),
			instance:   instance,
			background: colorButtonFace + 1,
			className:  className,
		}
		class.size = uint32(unsafe.Sizeof(class))
		if atom, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
			askTextClassRegistration = err
		}
	})
	if askTextClassRegistration != nil {
		return "", false
	}

	const width, height = 420, 196
	screenWidth, _, _ := procGetSystemMetrics.Call(smScreenWidth)
	screenHeight, _, _ := procGetSystemMetrics.Call(smScreenHeight)
	hwnd, _, _ := procCreateWindowEx.Call(
		wsExDialogOnTop, utf16(askTextClass), utf16(title), wsCaptioned,
		(screenWidth-width)/2, (screenHeight-height)/2, width, height,
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return "", false
	}
	font, _, _ := procGetStockObject.Call(defaultGUIFont)
	child := func(class, text string, style, x, y, w, h, id uintptr) uintptr {
		control, _, _ := procCreateWindowEx.Call(0, utf16(class), utf16(text), style, x, y, w, h, hwnd, id, instance, 0)
		procSendMessage.Call(control, wmSetFont, font, 1)
		return control
	}
	child("STATIC", prompt, wsLabel, 16, 14, 380, 52, 0)
	askTextEdit = child("EDIT", "", wsEdit, 16, 72, 380, 26, 0)
	procSendMessage.Call(askTextEdit, emSetLimitText, textLimit, 0)
	child("BUTTON", cancelLabel, wsButton, 188, 114, 100, 28, idCancel)
	child("BUTTON", okLabel, wsDefaultButton, 296, 114, 100, 28, idOK)
	askTextAnswer, askTextAccepted = "", false

	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
	procSetFocus.Call(askTextEdit)

	var message winMessage
	for {
		more, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(more) <= 0 {
			break
		}
		// Gives the window a dialog's keyboard: Tab, Enter (OK), Esc (Cancel).
		if handled, _, _ := procIsDialogMessage.Call(hwnd, uintptr(unsafe.Pointer(&message))); handled != 0 {
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
	return askTextAnswer, askTextAccepted
}
