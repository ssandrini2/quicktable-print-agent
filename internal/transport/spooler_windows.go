//go:build windows

package transport

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winspool             = windows.NewLazySystemDLL("winspool.drv")
	procOpenPrinter      = winspool.NewProc("OpenPrinterW")
	procClosePrinter     = winspool.NewProc("ClosePrinter")
	procStartDocPrinter  = winspool.NewProc("StartDocPrinterW")
	procEndDocPrinter    = winspool.NewProc("EndDocPrinter")
	procStartPagePrinter = winspool.NewProc("StartPagePrinter")
	procEndPagePrinter   = winspool.NewProc("EndPagePrinter")
	procWritePrinter     = winspool.NewProc("WritePrinter")
	procEnumPrinters     = winspool.NewProc("EnumPrintersW")
)

// DOC_INFO_1
type docInfo1 struct {
	docName    *uint16
	outputFile *uint16
	datatype   *uint16
}

// PRINTER_INFO_4
type printerInfo4 struct {
	printerName *uint16
	serverName  *uint16
	attributes  uint32
}

const (
	printerEnumLocal       = 0x00000002
	printerEnumConnections = 0x00000004
)

// sendSpooler queues data as a RAW job: the spooler passes the bytes to the
// printer untouched, whatever port (USB, serial, shared) it is on.
func sendSpooler(printerName string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	name, err := windows.UTF16PtrFromString(printerName)
	if err != nil {
		return err
	}
	var handle windows.Handle
	if ok, _, callErr := procOpenPrinter.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&handle)), 0); ok == 0 {
		return fmt.Errorf("opening printer %q: %w", printerName, callErr)
	}
	defer procClosePrinter.Call(uintptr(handle))

	docName, _ := windows.UTF16PtrFromString("QuickTable ticket")
	raw, _ := windows.UTF16PtrFromString("RAW")
	doc := docInfo1{docName: docName, datatype: raw}
	if job, _, callErr := procStartDocPrinter.Call(uintptr(handle), 1, uintptr(unsafe.Pointer(&doc))); job == 0 {
		return fmt.Errorf("starting the print job: %w", callErr)
	}
	defer procEndDocPrinter.Call(uintptr(handle))

	if ok, _, callErr := procStartPagePrinter.Call(uintptr(handle)); ok == 0 {
		return fmt.Errorf("starting the page: %w", callErr)
	}
	defer procEndPagePrinter.Call(uintptr(handle))

	var written uint32
	ok, _, callErr := procWritePrinter.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&written)),
	)
	if ok == 0 {
		return fmt.Errorf("sending the ticket: %w", callErr)
	}
	if int(written) != len(data) {
		return fmt.Errorf("the spooler took %d of %d bytes", written, len(data))
	}
	return nil
}

// InstalledPrinters lists the printers Windows has for this user: local ones
// and connections to shared ones.
func InstalledPrinters() ([]string, error) {
	const flags = printerEnumLocal | printerEnumConnections
	var needed, count uint32
	// First call sizes the buffer; it "fails" with ERROR_INSUFFICIENT_BUFFER.
	procEnumPrinters.Call(flags, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
	if needed == 0 {
		return nil, nil
	}
	buffer := make([]byte, needed)
	ok, _, callErr := procEnumPrinters.Call(
		flags, 0, 4,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(needed),
		uintptr(unsafe.Pointer(&needed)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ok == 0 {
		return nil, fmt.Errorf("listing printers: %w", callErr)
	}
	infos := unsafe.Slice((*printerInfo4)(unsafe.Pointer(&buffer[0])), count)
	names := make([]string, 0, count)
	for _, info := range infos {
		names = append(names, windows.UTF16PtrToString(info.printerName))
	}
	return names, nil
}
