package version

import "testing"

func TestCurrentReportsBuildInformation(t *testing.T) {
	info := Current()
	if info.App == "" {
		t.Fatal("app version is empty")
	}
	if info.Protocol != Protocol || info.Schema != Schema {
		t.Fatalf("protocol/schema = %d/%d", info.Protocol, info.Schema)
	}
	if info.Commit == "" || info.BuildTime == "" {
		t.Fatalf("commit/build time = %q/%q", info.Commit, info.BuildTime)
	}
	if info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("build platform = %#v", info)
	}
}

func TestLinkerValuesWin(t *testing.T) {
	oldCommit, oldBuildTime := Commit, BuildTime
	t.Cleanup(func() {
		Commit, BuildTime = oldCommit, oldBuildTime
	})
	Commit, BuildTime = "abc1234", "2026-10-30T00:00:00Z"
	info := Current()
	if info.Commit != Commit || info.BuildTime != BuildTime {
		t.Fatalf("linker values were overridden: %#v", info)
	}
}
