// Package transport gets a ticket's bytes to a printer.
package transport

import (
	"context"
	"fmt"
	"net"
	"time"
)

// How a printer is reached, as the API names it.
const (
	Network = "NETWORK" // raw ESC/POS over TCP; the address is host:port
	Windows = "WINDOWS" // through the Windows spooler; the address is the printer's name
)

const (
	dialTimeout  = 4 * time.Second
	writeTimeout = 10 * time.Second
)

// Send prints data on the printer at address.
func Send(ctx context.Context, connection, address string, data []byte) error {
	switch connection {
	case Network:
		return sendTCP(ctx, address, data)
	case Windows:
		return sendSpooler(address, data)
	default:
		return fmt.Errorf("unknown printer connection %q", connection)
	}
}

func sendTCP(ctx context.Context, address string, data []byte) error {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connecting to the printer: %w", err)
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("sending the ticket: %w", err)
	}
	return nil
}
