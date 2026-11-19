package domain

import "testing"

func TestValidateModFile(t *testing.T) {
	for name, enabled := range map[string]bool{"sodium-0.6.jar": true, "Mod [1.21] (x).jar.disabled": false} {
		got, err := ValidateModFile(name)
		if err != nil || got != enabled {
			t.Errorf("ValidateModFile(%q) = %v, %v", name, got, err)
		}
	}
	for _, name := range []string{"", ".jar", ".hidden.jar", "../x.jar", "a/b.jar", `a\b.jar`, "x.zip", "x.jar.old", ".jar.disabled", "a\x00.jar"} {
		if _, err := ValidateModFile(name); err == nil {
			t.Errorf("ValidateModFile(%q) accepted", name)
		}
	}
}

func TestSafeModFileName(t *testing.T) {
	for in, want := range map[string]string{
		"sodium-fabric-0.6.0+mc1.21.1.jar": "sodium-fabric-0.6.0+mc1.21.1.jar",
		"My Mod [1.21].jar":                "My_Mod__1.21_.jar",
		"../../evil.jar":                   "_.._evil.jar",
		"...hidden.jar":                    "hidden.jar",
		"émoji✓.jar":                       "_moji_.jar",
	} {
		if got, err := SafeModFileName(in); err != nil || got != want {
			t.Errorf("SafeModFileName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"mod.zip", "", "....jar"} {
		if got, err := SafeModFileName(in); err == nil {
			t.Errorf("SafeModFileName(%q) = %q, want an error", in, got)
		}
	}
}

func TestValidateModrinthID(t *testing.T) {
	if ValidateModrinthID("project_id", "AANobbMI") != nil {
		t.Error("valid ID refused")
	}
	for _, id := range []string{"", "short", "AANobbMI1", "../aaaaa", "AANobb-I"} {
		if ValidateModrinthID("project_id", id) == nil {
			t.Errorf("%q accepted", id)
		}
	}
}
