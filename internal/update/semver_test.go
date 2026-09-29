package update

import "testing"

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3+build.5", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"0.11.0", "0.12.0", -1},
		{"1.2.0-rc.1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc.1", 1},
		{"1.2.0-alpha", "1.2.0-alpha.1", -1},
		{"1.2.0-alpha.1", "1.2.0-alpha.beta", -1},
		{"1.2.0-alpha.beta", "1.2.0-beta", -1},
		{"1.2.0-beta.2", "1.2.0-beta.11", -1},
		{"1.2.0-rc.1", "1.1.9", 1},
	} {
		got, err := Compare(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
}

func TestCompareRejectsInvalid(t *testing.T) {
	for _, v := range []string{"", "dev", "1.2", "1.2.3.4", "1.x.3", "01.2.3", "1.2.3-", "1.2.3-a..b"} {
		if _, err := Compare(v, "1.0.0"); err == nil {
			t.Errorf("Compare(%q) accepted an invalid version", v)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		candidate, current string
		want               bool
	}{
		{"0.12.0", "0.11.0", true},
		{"v0.12.0", "0.11.0", true},
		{"0.11.0", "0.11.0", false},
		{"0.10.0", "0.11.0", false},
		// A pre-release is never offered over a stable release, even a
		// higher one...
		{"0.12.0-rc.1", "0.11.0", false},
		// ...but a stable release is offered over its own pre-release,
		// and someone on a pre-release sees the next one.
		{"0.12.0", "0.12.0-rc.1", true},
		{"0.12.0-rc.2", "0.12.0-rc.1", true},
	} {
		got, err := Newer(tc.candidate, tc.current)
		if err != nil || got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, %v; want %v", tc.candidate, tc.current, got, err, tc.want)
		}
	}
	if _, err := Newer("0.12.0", "dev"); err == nil {
		t.Error("Newer against dev should fail")
	}
}
