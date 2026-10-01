//go:build !windows

package transport

import "errors"

func sendSpooler(string, []byte) error {
	return errors.New("printing through the Windows spooler needs Windows")
}

// InstalledPrinters lists the printers Windows has; none on other systems.
func InstalledPrinters() ([]string, error) { return nil, nil }
