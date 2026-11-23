package domain

import "testing"

func TestModpackGame(t *testing.T) {
	for _, tc := range []struct {
		tags       []string
		loader, mc string
		ok         bool
	}{
		{[]string{"fabric", "1.21.1"}, LoaderFabric, "1.21.1", true},
		{[]string{"1.20.1", "Forge", "Client"}, LoaderForge, "1.20.1", true}, // CurseForge tags
		{[]string{"Forge", "NeoForge", "1.21.1"}, LoaderNeoForge, "1.21.1", true},
		{[]string{"fabric", "1.20", "1.20.1"}, LoaderFabric, "1.20.1", true},
		{[]string{"quilt", "1.20.1"}, "", "1.20.1", false},
		{[]string{"fabric", "24w10a"}, LoaderFabric, "", false},
		{nil, "", "", false},
	} {
		loader, mc, ok := ModpackGame(tc.tags)
		if loader != tc.loader || mc != tc.mc || ok != tc.ok {
			t.Errorf("ModpackGame(%v) = %q, %q, %v", tc.tags, loader, mc, ok)
		}
	}
}

func TestValidateModpackID(t *testing.T) {
	for _, tc := range []struct {
		source, id string
		ok         bool
	}{
		{ModpackSourceModrinth, "AbCd1234", true},
		{ModpackSourceModrinth, "123", false},
		{ModpackSourceCurseForge, "123456", true},
		{ModpackSourceCurseForge, "0123", false},
		{ModpackSourceCurseForge, "12;rm", false},
		{"other", "AbCd1234", false},
	} {
		if err := ValidateModpackID(tc.source, "id", tc.id); (err == nil) != tc.ok {
			t.Errorf("ValidateModpackID(%q, %q) = %v", tc.source, tc.id, err)
		}
	}
	if !ValidProjectSlug("all-the-mods-9") || ValidProjectSlug("Bad Slug") || ValidProjectSlug("") {
		t.Error("ValidProjectSlug")
	}
}

func TestValidateCurseForgeKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"$2a$10$abcDEF./xyz": true,
		"":                   false,
		"with space":         false,
		"tab\tkey":           false,
		"ключ":               false,
	} {
		if err := ValidateCurseForgeKey(key); (err == nil) != ok {
			t.Errorf("ValidateCurseForgeKey(%q) = %v", key, err)
		}
	}
}
