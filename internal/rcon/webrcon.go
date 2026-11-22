package rcon

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// webClient speaks Facepunch's WebRCON: JSON messages over a WebSocket,
// with the password as the URL path. It implements just enough of RFC 6455
// for that: text frames, fragmentation, ping and close.
type webClient struct {
	conn    net.Conn
	r       *bufio.Reader
	nextID  int
	timeout time.Duration
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func dialWebRCON(o Options) (Client, error) {
	conn, err := net.DialTimeout("tcp", o.Addr, o.Timeout)
	if err != nil {
		return nil, fmt.Errorf("connect to WebRCON at %s: %w", o.Addr, err)
	}
	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	conn.SetDeadline(time.Now().Add(o.Timeout))
	path := "/" + url.PathEscape(o.Password)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, o.Addr, key)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		// Rust drops the connection without a reply when the password is
		// wrong.
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w (the server closed the connection during login)", ErrAuth)
		}
		return nil, fmt.Errorf("WebRCON handshake: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		// Rust answers a wrong password by refusing the upgrade.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			return nil, ErrAuth
		}
		return nil, fmt.Errorf("WebRCON handshake: HTTP %s", resp.Status)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, errors.New("WebRCON handshake: bad Sec-WebSocket-Accept")
	}
	conn.SetDeadline(time.Time{})
	// Identifiers start high so they don't clash with the server's own
	// broadcast messages, which use small numbers.
	return &webClient{conn: conn, r: r, nextID: 1000, timeout: o.Timeout}, nil
}

type webMessage struct {
	Identifier int    `json:"Identifier"`
	Message    string `json:"Message"`
	Name       string `json:"Name,omitempty"`
	Type       string `json:"Type,omitempty"`
}

func (c *webClient) Exec(command string) (string, error) {
	if err := checkCommand(command); err != nil {
		return "", err
	}
	id := c.nextID
	c.nextID++
	b, _ := json.Marshal(webMessage{Identifier: id, Message: command, Name: "WebRcon"})
	if err := c.writeFrame(0x1, b); err != nil {
		return "", err
	}
	var out []string
	deadline := time.Now().Add(c.timeout)
	for {
		msg, err := c.readMessage(deadline)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return strings.Join(out, "\n"), nil
			}
			return strings.Join(out, "\n"), err
		}
		var m webMessage
		if json.Unmarshal(msg, &m) != nil || m.Identifier != id {
			continue // server log broadcasts and other chatter
		}
		out = append(out, strings.TrimRight(m.Message, "\n"))
		deadline = time.Now().Add(250 * time.Millisecond)
	}
}

// writeFrame sends one masked frame, as clients must.
func (c *webClient) writeFrame(opcode byte, payload []byte) error {
	var hdr []byte
	hdr = append(hdr, 0x80|opcode)
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.conn.Write(append(hdr, masked...))
	return err
}

// readMessage returns the next complete text message, answering pings.
func (c *webClient) readMessage(deadline time.Time) ([]byte, error) {
	c.conn.SetReadDeadline(deadline)
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return nil, err
		}
		fin, opcode := h[0]&0x80 != 0, h[0]&0x0f
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > 16<<20 {
			return nil, errors.New("WebRCON message too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.r, mask[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.r, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch opcode {
		case 0x8:
			return nil, errors.New("the server closed the WebRCON connection")
		case 0x9:
			if err := c.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0xA:
			continue
		case 0x1, 0x2, 0x0:
			msg = append(msg, payload...)
			if fin {
				return msg, nil
			}
		}
	}
}

func (c *webClient) Close() error {
	c.writeFrame(0x8, []byte{0x03, 0xe8})
	return c.conn.Close()
}
