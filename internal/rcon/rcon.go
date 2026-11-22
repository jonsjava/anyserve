// Package rcon talks to game servers' remote consoles.
//
// Two protocols are supported:
//   - source: Valve's Source RCON over TCP, used by Counter-Strike,
//     Minecraft, ARK, Conan Exiles, Rust (with rcon.web 0) and many others.
//   - webrcon: Facepunch's JSON-over-WebSocket RCON, Rust's default.
package rcon

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Protocols lists the supported protocol names.
var Protocols = []string{"source", "webrcon"}

// Client is a connected, authenticated remote console.
type Client interface {
	// Exec runs one command and returns the server's reply.
	Exec(command string) (string, error)
	Close() error
}

// Options configures a connection.
type Options struct {
	Protocol string
	Addr     string // host:port
	Password string
	// Timeout bounds connecting and waiting for the first reply.
	Timeout time.Duration
}

// ErrAuth means the server rejected the password.
var ErrAuth = errors.New("the server rejected the RCON password")

// Dial connects and logs in.
func Dial(o Options) (Client, error) {
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	switch o.Protocol {
	case "source":
		return dialSource(o)
	case "webrcon":
		return dialWebRCON(o)
	}
	return nil, fmt.Errorf("unknown RCON protocol %q (known: %s)", o.Protocol, strings.Join(Protocols, ", "))
}

// Known reports whether a protocol name is supported.
func Known(p string) bool {
	for _, k := range Protocols {
		if k == p {
			return true
		}
	}
	return false
}

// checkCommand rejects input that can't be a single console command.
func checkCommand(c string) error {
	if strings.ContainsAny(c, "\x00\r\n") {
		return errors.New("RCON commands must be a single line")
	}
	return nil
}
