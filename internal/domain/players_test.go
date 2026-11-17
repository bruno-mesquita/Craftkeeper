package domain

import "testing"

func TestOfflineUUID(t *testing.T) {
	if got := OfflineUUID("Notch"); got != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("OfflineUUID(Notch) = %s", got)
	}
}

func TestSetProperty(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "white-list=true\n"},
		{"a=1", "a=1\nwhite-list=true\n"},
		{"#c\r\nwhite-list=false\r\nb=2\r\n", "#c\r\nwhite-list=true\r\nb=2\r\n"},
		{"x-white-list=1\n", "x-white-list=1\nwhite-list=true\n"},
	} {
		if got := SetProperty(tc.in, "white-list", "true"); got != tc.want {
			t.Errorf("SetProperty(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if v, ok := PropertyValue("a=1\r\nonline-mode=false\r\n", "online-mode"); !ok || v != "false" {
		t.Errorf("PropertyValue = %q, %v", v, ok)
	}
}

func TestPlayerValidation(t *testing.T) {
	for _, n := range []string{"", "a b", "seventeen_chars_xx", "ñ"} {
		if ValidatePlayerName(n) == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if ip, err := NormalizeIP("::ffff:203.0.113.7"); err != nil || ip != "203.0.113.7" {
		t.Errorf("NormalizeIP = %q, %v", ip, err)
	}
	if _, err := NormalizeIP("fe80::1%eth0"); err == nil {
		t.Error("zoned address accepted")
	}
	if ValidateBanReason("a\nb") == nil {
		t.Error("multi-line reason accepted")
	}
}

func TestPlayerListRoundTrip(t *testing.T) {
	l, err := ParsePlayerList[Player]("whitelist.json", "")
	if err != nil || l == nil || len(l) != 0 {
		t.Fatalf("empty = %v, %v", l, err)
	}
	if _, err := ParsePlayerList[Player]("whitelist.json", "{"); err == nil {
		t.Fatal("garbage accepted")
	}
	out := EncodePlayerList([]Player{{UUID: "u", Name: "n"}})
	back, err := ParsePlayerList[Player]("whitelist.json", out)
	if err != nil || len(back) != 1 || back[0].Name != "n" || EncodePlayerList([]Player{}) != "[]\n" {
		t.Fatalf("round trip = %v, %v", back, err)
	}
}
