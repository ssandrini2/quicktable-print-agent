// A stand-in for a network thermal printer, for developing without one: it
// listens on TCP like a real printer (port 9100), and shows every ticket it
// receives as text — on the console and in a file per ticket.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/quicktable/print-agent/internal/escpos"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9100", "address to listen on")
	out := flag.String("out", "tickets", "folder for the received tickets (.txt as text, .bin as sent); empty to keep none")
	flag.Parse()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("could not listen on %s: %v", *addr, err)
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("Virtual printer listening on %s\n", listener.Addr())
	fmt.Printf("In the admin, add a network printer with address %s\n\n", listener.Addr())

	var count atomic.Int64
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go receive(conn, *out, count.Add(1))
	}
}

// receive reads one print job — everything the sender writes until it closes
// the connection — and shows it.
func receive(conn net.Conn, out string, number int64) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil && len(data) == 0 {
		log.Printf("ticket %d: %v", number, err)
		return
	}
	text := escpos.Preview(data)
	now := time.Now()
	fmt.Printf("===== ticket %d · %s · %d bytes =====\n%s\n", number, now.Format("15:04:05"), len(data), text)

	if out == "" {
		return
	}
	base := filepath.Join(out, fmt.Sprintf("%s-%03d", now.Format("20060102-150405"), number))
	if err := os.WriteFile(base+".txt", []byte(text), 0o644); err != nil {
		log.Printf("ticket %d: %v", number, err)
	}
	if err := os.WriteFile(base+".bin", data, 0o644); err != nil {
		log.Printf("ticket %d: %v", number, err)
	}
}
