// Package password implements Argon2id hashing in PHC string format:
// $argon2id$v=19$m=<KiB>,t=<iterations>,p=<threads>$<salt b64>$<hash b64>
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id cost parameters.
type Params struct {
	MemoryKiB  uint32
	Iterations uint32
	Threads    uint8
}

// DefaultParams follow the OWASP minimum (19 MiB, t=2, p=1): strong enough for an admin
// panel while keeping peak RAM per login low.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Iterations: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// maxConcurrent caps simultaneous hash computations so login floods cannot exhaust RAM.
	maxConcurrent = 2
)

var errMalformed = errors.New("password: malformed argon2id hash")

type Argon2id struct {
	params Params
	sem    chan struct{}
}

func NewArgon2id(p Params) *Argon2id {
	return &Argon2id{params: p, sem: make(chan struct{}, maxConcurrent)}
}

func (a *Argon2id) Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := a.derive(password, salt, a.params, keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, a.params.MemoryKiB, a.params.Iterations, a.params.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify checks password against encoded using the parameters stored in encoded, so
// hashes stay verifiable after DefaultParams change.
func (a *Argon2id) Verify(password, encoded string) (bool, error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}
	got := a.derive(password, salt, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func (a *Argon2id) derive(password string, salt []byte, p Params, n uint32) []byte {
	a.sem <- struct{}{}
	defer func() { <-a.sem }()
	return argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Threads, n)
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, errMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, errMalformed
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Threads); err != nil {
		return Params{}, nil, nil, errMalformed
	}
	// Refuse absurd stored parameters (e.g. a tampered DB row) that would exhaust RAM/CPU.
	if p.MemoryKiB == 0 || p.MemoryKiB > 1<<20 || p.Iterations == 0 || p.Iterations > 16 || p.Threads == 0 {
		return Params{}, nil, nil, errMalformed
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, errMalformed
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return Params{}, nil, nil, errMalformed
	}
	return p, salt, key, nil
}
