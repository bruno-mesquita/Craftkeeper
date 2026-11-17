package password

import (
	"strings"
	"testing"
)

var fast = Params{MemoryKiB: 64, Iterations: 1, Threads: 1}

func TestHashAndVerify(t *testing.T) {
	a := NewArgon2id(fast)
	h, err := a.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if ok, err := a.Verify("correct horse battery staple", h); err != nil || !ok {
		t.Fatalf("verify correct = %v, %v", ok, err)
	}
	if ok, err := a.Verify("wrong", h); err != nil || ok {
		t.Fatalf("verify wrong = %v, %v", ok, err)
	}
	h2, _ := a.Hash("correct horse battery staple")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestVerifyUsesStoredParams(t *testing.T) {
	h, _ := NewArgon2id(fast).Hash("pw")
	if ok, _ := NewArgon2id(DefaultParams).Verify("pw", h); !ok {
		t.Fatal("hash made with other params must still verify")
	}
}

func TestVerifyRejectsMalformedAndAbusiveHashes(t *testing.T) {
	a := NewArgon2id(fast)
	for _, enc := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if _, err := a.Verify("pw", enc); err == nil {
			t.Errorf("expected error for %q", enc)
		}
	}
}
