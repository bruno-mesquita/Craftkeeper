package domain

import "testing"

func TestValidateLoader(t *testing.T) {
	for _, l := range []string{"vanilla", "fabric", "forge", "neoforge"} {
		if err := ValidateLoader(l); err != nil {
			t.Errorf("%q: %v", l, err)
		}
	}
	for _, l := range []string{"", "paper", "Forge", "forge "} {
		if err := ValidateLoader(l); err == nil {
			t.Errorf("%q accepted", l)
		}
	}
}

func TestValidLoaderVersion(t *testing.T) {
	for _, v := range []string{"0.16.10", "47.3.0", "14.23.5.2859", "21.1.77", "20.4.80-beta", "26.3.0.110"} {
		if !ValidLoaderVersion(v) {
			t.Errorf("%q rejected", v)
		}
	}
	for _, v := range []string{"", "1", "latest", "47.3.0;rm -rf /", "1.2.3.4.5", "47.3.0-rc1", "1.7.10-10.13.4", "$(id)", "47.3.0\n"} {
		if ValidLoaderVersion(v) {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestIsMinecraftRelease(t *testing.T) {
	if IsMinecraftRelease("LATEST") || IsMinecraftRelease("26.4-snapshot-10") || !IsMinecraftRelease("1.20.1") || !IsMinecraftRelease("26.3") {
		t.Fatal("unexpected classification")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.21", "1.21.0", 0},
		{"1.21.10", "1.21.9", 1},
		{"1.9", "1.10", -1},
		{"26.1", "1.21.11", 1},
		{"21.0.0-beta", "21.0.0", -1},
		{"21.0.1-beta", "21.0.0", 1},
		{"20.4.80-beta", "20.4.80-beta", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestJavaFor(t *testing.T) {
	cases := map[string]string{
		"1.7.10": Java8, "1.12.2": Java8, "1.16.5": Java8,
		"1.17": Java17, "1.18.2": Java17, "1.20.1": Java17, "1.20.4": Java17,
		"1.20.5": Java21, "1.21": Java21, "1.21.1": Java21, "1.21.11": Java21,
		"26.1": Java25, "26.3": Java25, "LATEST": Java25,
	}
	for v, want := range cases {
		if got := JavaFor(v); got != want {
			t.Errorf("JavaFor(%q) = %s, want %s", v, got, want)
		}
	}
}
