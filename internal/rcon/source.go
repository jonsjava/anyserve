package rcon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Source RCON packet types.
const (
	typeResponse = 0
	typeExec     = 2
	typeAuthResp = 2
	typeAuth     = 3
)

// maxPacket is the protocol's packet size limit (body up to 4096 bytes
// from the server; we allow a little more for lenient servers).
const maxPacket = 1 << 16

type sourceClient struct {
	conn    net.Conn
	r       *bufio.Reader
	nextID  int32
	timeout time.Duration
}

func dialSource(o Options) (Client, error) {
	conn, err := net.DialTimeout("tcp", o.Addr, o.Timeout)
	if err != nil {
		return nil, fmt.Errorf("connect to RCON at %s: %w", o.Addr, err)
	}
	c := &sourceClient{conn: conn, r: bufio.NewReader(conn), nextID: 1, timeout: o.Timeout}
	if err := c.auth(o.Password); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *sourceClient) write(id, typ int32, body string) error {
	if len(body) > 4096 {
		return errors.New("RCON command is too long")
	}
	buf := make([]byte, 12+len(body)+2)
	binary.LittleEndian.PutUint32(buf[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.conn.Write(buf)
	return err
}

type packet struct {
	id, typ int32
	body    string
}

func (c *sourceClient) read(deadline time.Time) (packet, error) {
	c.conn.SetReadDeadline(deadline)
	var hdr [4]byte
	if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
		return packet{}, err
	}
	size := int32(binary.LittleEndian.Uint32(hdr[:]))
	if size < 10 || size > maxPacket {
		return packet{}, fmt.Errorf("bad RCON packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return packet{}, err
	}
	body := strings.TrimRight(string(buf[8:]), "\x00")
	return packet{
		id:   int32(binary.LittleEndian.Uint32(buf[0:])),
		typ:  int32(binary.LittleEndian.Uint32(buf[4:])),
		body: body,
	}, nil
}

func (c *sourceClient) auth(password string) error {
	id := c.nextID
	c.nextID++
	if err := c.write(id, typeAuth, password); err != nil {
		return err
	}
	deadline := time.Now().Add(c.timeout)
	for {
		p, err := c.read(deadline)
		if err != nil {
			return fmt.Errorf("RCON login: %w", err)
		}
		// Some servers send an empty response before the auth result.
		if p.typ != typeAuthResp {
			continue
		}
		if p.id == -1 {
			return ErrAuth
		}
		if p.id == id {
			return nil
		}
	}
}

// Exec sends a command and collects its reply. Long replies arrive in
// several packets, so after the first one it keeps reading until the
// server goes quiet briefly.
func (c *sourceClient) Exec(command string) (string, error) {
	if err := checkCommand(command); err != nil {
		return "", err
	}
	id := c.nextID
	c.nextID++
	if err := c.write(id, typeExec, command); err != nil {
		return "", err
	}
	var out strings.Builder
	deadline := time.Now().Add(c.timeout)
	got := false
	for {
		p, err := c.read(deadline)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if got {
					return out.String(), nil
				}
				// Some commands produce no output at all.
				return "", nil
			}
			return out.String(), err
		}
		if p.typ == typeAuthResp && p.id == -1 {
			return "", ErrAuth
		}
		if p.id == id && p.typ == typeResponse {
			out.WriteString(p.body)
			got = true
			deadline = time.Now().Add(250 * time.Millisecond)
		}
	}
}

func (c *sourceClient) Close() error { return c.conn.Close() }
