package rcon

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeSource is a minimal Source RCON server.
func fakeSource(t *testing.T, password string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				send := func(id, typ int32, body string) {
					b := make([]byte, 14+len(body))
					binary.LittleEndian.PutUint32(b, uint32(10+len(body)))
					binary.LittleEndian.PutUint32(b[4:], uint32(id))
					binary.LittleEndian.PutUint32(b[8:], uint32(typ))
					copy(b[12:], body)
					c.Write(b)
				}
				for {
					var hdr [4]byte
					if _, err := io.ReadFull(c, hdr[:]); err != nil {
						return
					}
					buf := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
					if _, err := io.ReadFull(c, buf); err != nil {
						return
					}
					id := int32(binary.LittleEndian.Uint32(buf))
					typ := int32(binary.LittleEndian.Uint32(buf[4:]))
					body := strings.TrimRight(string(buf[8:]), "\x00")
					switch typ {
					case 3:
						send(id, 0, "") // like srcds, an empty response first
						if body == password {
							send(id, 2, "")
						} else {
							send(-1, 2, "")
						}
					case 2:
						switch body {
						case "status":
							// A reply split over two packets.
							send(id, 0, "hostname: test\n")
							send(id, 0, "players: 0\n")
						case "zero":
							// Palworld replies with id 0, not the request's id.
							send(0, 0, "zero id")
						case "quiet":
						default:
							send(id, 0, "echo "+body)
						}
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestSource(t *testing.T) {
	addr := fakeSource(t, "pw")
	c, err := Dial(Options{Protocol: "source", Addr: addr, Password: "pw", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := c.Exec("status"); err != nil || got != "hostname: test\nplayers: 0\n" {
		t.Errorf("status = %q, %v", got, err)
	}
	if got, err := c.Exec("say hi"); err != nil || got != "echo say hi" {
		t.Errorf("say = %q, %v", got, err)
	}
	if got, err := c.Exec("zero"); err != nil || got != "zero id" {
		t.Errorf("zero = %q, %v", got, err)
	}
	if got, err := c.Exec("quiet"); err != nil || got != "" {
		t.Errorf("quiet = %q, %v", got, err)
	}
	if _, err := c.Exec("two\nlines"); err == nil {
		t.Error("multi-line command should be refused")
	}
	if _, err := Dial(Options{Protocol: "source", Addr: addr, Password: "wrong", Timeout: time.Second}); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong password: got %v", err)
	}
}

// fakeWebRCON is a minimal Rust-style WebRCON server.
func fakeWebRCON(t *testing.T, password string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+password {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		rw.Flush()
		write := func(m webMessage) {
			b, _ := json.Marshal(m)
			hdr := []byte{0x81}
			if len(b) < 126 {
				hdr = append(hdr, byte(len(b)))
			} else {
				hdr = append(hdr, 126, byte(len(b)>>8), byte(len(b)))
			}
			conn.Write(append(hdr, b...))
		}
		r2 := bufio.NewReader(rw)
		for {
			var h [2]byte
			if _, err := io.ReadFull(r2, h[:]); err != nil {
				return
			}
			n := int(h[1] & 0x7f)
			if n == 126 {
				var b [2]byte
				io.ReadFull(r2, b[:])
				n = int(binary.BigEndian.Uint16(b[:]))
			}
			var mask [4]byte
			io.ReadFull(r2, mask[:])
			p := make([]byte, n)
			io.ReadFull(r2, p)
			for i := range p {
				p[i] ^= mask[i%4]
			}
			if h[0]&0x0f == 0x8 {
				return
			}
			var m webMessage
			json.Unmarshal(p, &m)
			// A broadcast log line the client must ignore.
			write(webMessage{Identifier: 0, Message: "[log] someone joined", Type: "Generic"})
			write(webMessage{Identifier: m.Identifier, Message: "ran " + m.Message, Type: "Generic"})
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func TestWebRCON(t *testing.T) {
	addr := fakeWebRCON(t, "p@ss word")
	c, err := Dial(Options{Protocol: "webrcon", Addr: addr, Password: "p@ss word", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := c.Exec("serverinfo"); err != nil || got != "ran serverinfo" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := Dial(Options{Protocol: "webrcon", Addr: addr, Password: "nope", Timeout: time.Second}); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong password: got %v", err)
	}
}
