package update

import "testing"

func TestParseSemVer(t *testing.T) {
	valid := []string{
		"0.0.0",
		"1.2.3",
		"1.2.3-alpha",
		"1.2.3-alpha.1",
		"1.2.3-0.3.7",
		"1.2.3-x.7.z.92",
		"1.2.3+20130313144700",
		"1.2.3-beta+exp.sha.5114f85",
	}
	for _, raw := range valid {
		version, err := ParseSemVer(raw)
		if err != nil {
			t.Errorf("ParseSemVer(%q): %v", raw, err)
			continue
		}
		if version.String() != raw {
			t.Errorf("ParseSemVer(%q).String() = %q", raw, version.String())
		}
	}
	invalid := []string{
		"", "v1.2.3", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03",
		"1.2.3-", "1.2.3+", "1.2.3-01", "1.2.3-alpha..1", " 1.2.3", "1.2.3 ",
		"-1.2.3", "1.2.x",
	}
	for _, raw := range invalid {
		if _, err := ParseSemVer(raw); err == nil {
			t.Errorf("ParseSemVer(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestCanonicalReleaseRejectsBuildMetadata(t *testing.T) {
	if _, err := canonicalRelease("1.2.3+build.1"); err == nil {
		t.Fatal("release with build metadata was accepted")
	}
	if _, err := canonicalRelease("1.2.3-beta.1"); err != nil {
		t.Fatalf("prerelease rejected: %v", err)
	}
	if _, err := canonicalRelease("v1.2.3"); err == nil {
		t.Fatal("release with v prefix was accepted")
	}
}

func TestSemVerCompare(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
		"1.0.1",
		"1.1.0",
		"2.0.0",
	}
	for i := 0; i < len(ordered)-1; i++ {
		a, err := ParseSemVer(ordered[i])
		if err != nil {
			t.Fatal(err)
		}
		b, err := ParseSemVer(ordered[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if a.Compare(b) != -1 || b.Compare(a) != 1 {
			t.Errorf("%s should sort before %s", ordered[i], ordered[i+1])
		}
	}
	a, _ := ParseSemVer("1.2.3+build.1")
	b, _ := ParseSemVer("1.2.3+build.2")
	if a.Compare(b) != 0 {
		t.Error("build metadata changed precedence")
	}
}

func TestSemVerCompareLargeNumericPrerelease(t *testing.T) {
	ordered := []string{
		"1.0.0-99999999999999999999999999999999999998",
		"1.0.0-99999999999999999999999999999999999999",
		"1.0.0-100000000000000000000000000000000000000",
	}
	for i := 0; i < len(ordered)-1; i++ {
		a, err := ParseSemVer(ordered[i])
		if err != nil {
			t.Fatal(err)
		}
		b, err := ParseSemVer(ordered[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if a.Compare(b) != -1 || b.Compare(a) != 1 {
			t.Errorf("%s should sort before %s", ordered[i], ordered[i+1])
		}
	}
}
