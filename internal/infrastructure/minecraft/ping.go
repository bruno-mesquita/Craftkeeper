// Package minecraft asks a running Minecraft Java server who is online, with the Server List
// Ping the game client uses for its server list. It only connects to the loopback address,
// where the panel's servers publish their game port.
package minecraft

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	host        = "127.0.0.1"
	pingTimeout = 3 * time.Second
	// The status JSON is a protocol string of at most 32767 UTF-16 units (≤ 131068 bytes).
	maxPacket = 256 << 10
	// Any protocol version works for a status request; -1 is what the wiki suggests for "unknown".
	protocolVersion = -1
	nextStateStatus = 1
	zeroUUID        = "00000000-0000-0000-0000-000000000000"
)

var errProtocol = errors.New("minecraft: unexpected status response")

type Pinger struct {
	dialer net.Dialer
}

func New() *Pinger { return &Pinger{} }

// Status returns the online and maximum player counts and the names the game reports.
// Vanilla lists at most 12 players, and none with hide-online-players=true.
func (p *Pinger) Status(ctx context.Context, port int) (domain.OnlinePlayers, error) {
	if port < 1 || port > 65535 {
		return domain.OnlinePlayers{}, fmt.Errorf("minecraft: invalid port %d", port)
	}
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	conn, err := p.dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return domain.OnlinePlayers{}, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	var hs bytes.Buffer
	hs.WriteByte(0x00) // handshake
	hs.Write(varInt(protocolVersion))
	hs.Write(varInt(int32(len(host))))
	hs.WriteString(host)
	_ = binary.Write(&hs, binary.BigEndian, uint16(port))
	hs.Write(varInt(nextStateStatus))
	req := append(varInt(int32(hs.Len())), hs.Bytes()...)
	req = append(req, 0x01, 0x00) // status request: length 1, id 0
	if _, err := conn.Write(req); err != nil {
		return domain.OnlinePlayers{}, err
	}

	payload, err := readStatus(bufio.NewReader(conn))
	if err != nil {
		return domain.OnlinePlayers{}, err
	}
	return parseStatus(payload)
}

// readStatus reads the status response packet and returns its JSON string.
func readStatus(r *bufio.Reader) ([]byte, error) {
	n, err := readVarInt(r)
	if err != nil {
		return nil, err
	}
	if n < 2 || n > maxPacket {
		return nil, errProtocol
	}
	pkt := make([]byte, n)
	if _, err := io.ReadFull(r, pkt); err != nil {
		return nil, err
	}
	br := bytes.NewReader(pkt)
	if id, err := readVarInt(br); err != nil || id != 0x00 {
		return nil, errProtocol
	}
	l, err := readVarInt(br)
	if err != nil || l < 0 || int(l) != br.Len() {
		return nil, errProtocol
	}
	return pkt[len(pkt)-int(l):], nil
}

type statusJSON struct {
	Players struct {
		Online int `json:"online"`
		Max    int `json:"max"`
		Sample []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"sample"`
	} `json:"players"`
}

// parseStatus keeps only real players from the sample: servers and mods put formatted text
// lines (with the zero UUID or invalid names) there too.
func parseStatus(payload []byte) (domain.OnlinePlayers, error) {
	var s statusJSON
	if err := json.Unmarshal(payload, &s); err != nil {
		return domain.OnlinePlayers{}, errProtocol
	}
	out := domain.OnlinePlayers{Online: max(s.Players.Online, 0), Max: max(s.Players.Max, 0), Players: []domain.Player{}}
	for _, p := range s.Players.Sample {
		uuid, err := domain.ParseUUID(p.ID)
		if err != nil || uuid == zeroUUID || domain.ValidatePlayerName(p.Name) != nil {
			continue
		}
		out.Players = append(out.Players, domain.Player{UUID: uuid, Name: p.Name})
	}
	return out, nil
}

func varInt(v int32) []byte {
	u := uint32(v)
	var b []byte
	for {
		if u&^0x7f == 0 {
			return append(b, byte(u))
		}
		b = append(b, byte(u&0x7f|0x80))
		u >>= 7
	}
}

func readVarInt(r io.ByteReader) (int32, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int32(v), nil
		}
	}
	return 0, errProtocol
}
