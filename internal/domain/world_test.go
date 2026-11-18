package domain

import "testing"

func TestLevelName(t *testing.T) {
	for props, want := range map[string]string{
		"":                            "world",
		"level-name=\n":               "world",
		"level-name=survival\r\n":     "survival",
		"level-name= maps/one/ \n":    "maps/one",
		"motd=x\nlevel-name=./w//x\n": "w/x",
	} {
		if got, err := LevelName(props); err != nil || got != want {
			t.Errorf("%q: %q, %v", props, got, err)
		}
	}
	for _, props := range []string{"level-name=../up\n", "level-name=.\n", "level-name=/\n", "level-name=a\\\\b\n"} {
		if got, err := LevelName(props); err == nil {
			t.Errorf("%q accepted as %q", props, got)
		}
	}
}

func TestOpLevel(t *testing.T) {
	for props, want := range map[string]int{"": 4, "op-permission-level=2\n": 2, "op-permission-level=9\n": 4, "op-permission-level=1\r\n": 1} {
		if got := OpLevel(props); got != want {
			t.Errorf("%q: %d", props, got)
		}
	}
}

func TestValidateSeed(t *testing.T) {
	for _, ok := range []string{"", "-42", "a seed with spaces", "🌱"} {
		if err := ValidateSeed(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a\nb", "tab\t", `back\slash`, string(make([]byte, 65))} {
		if err := ValidateSeed(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOpsRoundTrip(t *testing.T) {
	const file = `[
  {
    "uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5",
    "name": "Notch",
    "level": 4,
    "bypassesPlayerLimit": false
  }
]
`
	ops, err := ParsePlayerList[Operator](OpsFile, file)
	if err != nil || len(ops) != 1 || ops[0].Name != "Notch" || ops[0].Level != 4 {
		t.Fatalf("ops = %+v, %v", ops, err)
	}
	if got := EncodePlayerList(ops); got != file {
		t.Fatalf("encoded = %s", got)
	}
}
