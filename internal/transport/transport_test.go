package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
)

func TestSendTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		received <- data
	}()

	ticket := []byte{0x1B, '@', 'h', 'i', '\n'}
	if err := Send(context.Background(), Network, listener.Addr().String(), ticket); err != nil {
		t.Fatal(err)
	}

	if got := <-received; !bytes.Equal(got, ticket) {
		t.Fatalf("the printer got % x", got)
	}
}

func TestSendTCPFailsWhenThePrinterIsOff(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()

	if err := Send(context.Background(), Network, address, []byte("x")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSendRejectsAnUnknownConnection(t *testing.T) {
	if err := Send(context.Background(), "BLUETOOTH", "x", []byte("x")); err == nil {
		t.Fatal("expected an error")
	}
}

// Read-only: asks Windows for its printers, prints nothing.
func TestInstalledPrinters(t *testing.T) {
	names, err := InstalledPrinters()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "" {
			t.Error("a printer without a name")
		}
	}
	t.Logf("%d printers", len(names))
}
