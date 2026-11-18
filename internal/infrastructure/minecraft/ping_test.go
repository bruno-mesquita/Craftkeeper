package minecraft

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeServer answers one status ping with reply and returns the port it listens on.
func fakeServer(t *testing.T, reply func(t *testing.T, c net.Conn, port int)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		reply(t, c, port)
	}()
	return port
}

// readRequest checks the handshake and the status request the client sends.
func readRequest(t *testing.T, c net.Conn, port int) {
	r := bufio.NewReader(c)
	n, err := readVarInt(r)
	if err != nil {
		t.Error(err)
		return
	}
	hs := make([]byte, n)
	io.ReadFull(r, hs)
	br := bytes.NewReader(hs)
	id, _ := readVarInt(br)
	proto, _ := readVarInt(br)
	hl, _ := readVarInt(br)
	h := make([]byte, hl)
	br.Read(h)
	var p uint16
	binary.Read(br, binary.BigEndian, &p)
	next, _ := readVarInt(br)
	if id != 0 || proto != -1 || string(h) != "127.0.0.1" || int(p) != port || next != 1 || br.Len() != 0 {
		t.Errorf("handshake = id %d proto %d host %q port %d next %d", id, proto, h, p, next)
	}
	req := make([]byte, 2)
	io.ReadFull(r, req)
	if !bytes.Equal(req, []byte{1, 0}) {
		t.Errorf("status request = %v", req)
	}
}

func statusPacket(js string) []byte {
	body := append([]byte{0x00}, varInt(int32(len(js)))...)
	body = append(body, js...)
	return append(varInt(int32(len(body))), body...)
}

func TestStatus(t *testing.T) {
	const js = `{"version":{"name":"1.21.4","protocol":769},"description":{"text":"hi"},"players":{"online":3,"max":20,"sample":[
		{"name":"Notch","id":"069a79f4-44e9-4726-a5be-fca90e38aaf5"},
		{"name":"jeb_","id":"853C80EF3C3749FDAA49938B674ADAE6"},
		{"name":"§aWelcome!","id":"00000000-0000-0000-0000-000000000000"},
		{"name":"Anonymous Player","id":"00000000-0000-0000-0000-000000000000"},
		{"name":"Bad","id":"nope"}]}}`
	port := fakeServer(t, func(t *testing.T, c net.Conn, port int) {
		readRequest(t, c, port)
		c.Write(statusPacket(js))
	})
	got, err := New().Status(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	if got.Online != 3 || got.Max != 20 || len(got.Players) != 2 ||
		got.Players[0].Name != "Notch" || got.Players[0].UUID != "069a79f4-44e9-4726-a5be-fca90e38aaf5" ||
		got.Players[1].UUID != "853c80ef-3c37-49fd-aa49-938b674adae6" {
		t.Fatalf("status = %+v", got)
	}
}

func TestStatusWithoutSample(t *testing.T) {
	port := fakeServer(t, func(t *testing.T, c net.Conn, _ int) {
		io.CopyN(io.Discard, c, 1) // let the request arrive
		c.Write(statusPacket(`{"players":{"online":0,"max":10}}`))
	})
	got, err := New().Status(context.Background(), port)
	if err != nil || got.Online != 0 || got.Max != 10 || got.Players == nil || len(got.Players) != 0 {
		t.Fatalf("status = %+v, %v", got, err)
	}
}

func TestStatusRejectsBadResponses(t *testing.T) {
	for name, resp := range map[string][]byte{
		"not json":      statusPacket(`<html>`),
		"wrong id":      append(varInt(3), 0x01, 0x01, 'x'),
		"too large":     varInt(maxPacket + 1),
		"short length":  append(varInt(10), 0x00, 0x08, '{', '}'),
		"bad varint":    {0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"closed early":  {},
		"string length": append(varInt(4), 0x00, 0x05, '{', '}'),
	} {
		t.Run(name, func(t *testing.T) {
			port := fakeServer(t, func(t *testing.T, c net.Conn, _ int) {
				io.CopyN(io.Discard, c, 1)
				c.Write(resp)
			})
			if got, err := New().Status(context.Background(), port); err == nil {
				t.Fatalf("status = %+v, want error", got)
			}
		})
	}
}

func TestStatusTimesOut(t *testing.T) {
	port := fakeServer(t, func(t *testing.T, c net.Conn, _ int) {
		io.Copy(io.Discard, c) // never answers
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := New().Status(ctx, port); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("ping ignored the context deadline")
	}
}

func TestStatusClosedPort(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if _, err := New().Status(context.Background(), port); err == nil {
		t.Fatal("expected connection error")
	}
	if _, err := New().Status(context.Background(), 0); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestVarInt(t *testing.T) {
	for _, v := range []int32{0, 1, 127, 128, 255, 25565, 2147483647, -1, -2147483648} {
		got, err := readVarInt(bytes.NewReader(varInt(v)))
		if err != nil || got != v {
			t.Errorf("varint %d round-trip = %d, %v", v, got, err)
		}
	}
	if b := varInt(-1); !bytes.Equal(b, []byte{0xff, 0xff, 0xff, 0xff, 0x0f}) {
		t.Errorf("varInt(-1) = %x", b)
	}
}
