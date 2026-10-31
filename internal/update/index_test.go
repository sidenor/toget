package update

import (
	"strings"
	"testing"
	"time"
)

func testIndexEntries() []IndexEntry {
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Format(time.RFC3339)
	return []IndexEntry{
		{Release: "1.0.0", Manifest: "v1.0.0.json", ManifestSHA256: strings.Repeat("a", 64), Level: LevelRecommended, PublishedAt: now},
		{Release: "1.2.0", Manifest: "v1.2.0.json", ManifestSHA256: strings.Repeat("b", 64), Level: LevelSecurity, PublishedAt: now,
			Yanked: true, YankedAt: now, YankReason: "bad build"},
		{Release: "1.1.0", Manifest: "v1.1.0.json", ManifestSHA256: strings.Repeat("c", 64), Level: LevelForced, PublishedAt: now},
	}
}

func TestReleaseIndexBuildAndVerify(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	index, err := BuildReleaseIndex(BuildReleaseIndexOptions{
		Entries:     testIndexEntries(),
		PublishedAt: time.Now().UTC().Add(-time.Minute),
		Key:         key,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Releases) != 3 || index.Releases[0].Release != "1.2.0" || index.Releases[2].Release != "1.0.0" {
		t.Fatalf("entries = %+v", index.Releases)
	}
	if index.Latest != "1.1.0" {
		t.Fatalf("latest = %q", index.Latest)
	}
	latest := index.LatestEntry()
	if latest == nil || latest.Release != "1.1.0" {
		t.Fatalf("latest entry = %+v", latest)
	}
	raw := mustJSON(t, index)
	verified, err := verifier.VerifyReleaseIndex(raw)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Latest != "1.1.0" || len(verified.Releases) != 3 {
		t.Fatalf("verified = %+v", verified)
	}
}

func TestReleaseIndexAllYanked(t *testing.T) {
	_, key, cert, _ := testFundingKeys(t)
	entries := testIndexEntries()
	for i := range entries {
		entries[i].Yanked = true
		entries[i].YankedAt = entries[i].PublishedAt
	}
	index, err := BuildReleaseIndex(BuildReleaseIndexOptions{Entries: entries, Key: key, Cert: cert})
	if err != nil {
		t.Fatal(err)
	}
	if index.Latest != "" {
		t.Fatalf("latest = %q, want empty", index.Latest)
	}
	if index.LatestEntry() != nil {
		t.Fatal("LatestEntry returned an entry for an all-yanked index")
	}
}

func TestReleaseIndexRejectsBadEntries(t *testing.T) {
	_, key, cert, _ := testFundingKeys(t)
	cases := map[string][]IndexEntry{
		"empty": nil,
		"duplicate": append(testIndexEntries(), IndexEntry{
			Release: "1.0.0", Manifest: "v1.0.0.json", ManifestSHA256: strings.Repeat("d", 64),
			Level: LevelRecommended, PublishedAt: time.Now().UTC().Format(time.RFC3339),
		}),
		"leading v": {{
			Release: "v1.0.0", Manifest: "v1.0.0.json", ManifestSHA256: strings.Repeat("a", 64),
			Level: LevelRecommended, PublishedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		"bad hash": {{
			Release: "1.0.0", Manifest: "v1.0.0.json", ManifestSHA256: "nope",
			Level: LevelRecommended, PublishedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		"path": {{
			Release: "1.0.0", Manifest: "../v1.0.0.json", ManifestSHA256: strings.Repeat("a", 64),
			Level: LevelRecommended, PublishedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		"bad level": {{
			Release: "1.0.0", Manifest: "v1.0.0.json", ManifestSHA256: strings.Repeat("a", 64),
			Level: "critical", PublishedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		"yank metadata without yank": {{
			Release: "1.0.0", Manifest: "v1.0.0.json", ManifestSHA256: strings.Repeat("a", 64),
			Level: LevelRecommended, PublishedAt: time.Now().UTC().Format(time.RFC3339), YankedAt: time.Now().UTC().Format(time.RFC3339),
		}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildReleaseIndex(BuildReleaseIndexOptions{Entries: entries, Key: key, Cert: cert}); err == nil {
				t.Fatal("BuildReleaseIndex accepted invalid entries")
			}
		})
	}
}

func TestReleaseIndexRejectsTampering(t *testing.T) {
	_, key, cert, verifier := testFundingKeys(t)
	index, err := BuildReleaseIndex(BuildReleaseIndexOptions{Entries: testIndexEntries(), Key: key, Cert: cert})
	if err != nil {
		t.Fatal(err)
	}
	index.Releases[0].Level = LevelRecommended
	if _, err := verifier.VerifyReleaseIndex(mustJSON(t, index)); err == nil {
		t.Fatal("tampered index was accepted")
	}

	index, err = BuildReleaseIndex(BuildReleaseIndexOptions{Entries: testIndexEntries(), Key: key, Cert: cert})
	if err != nil {
		t.Fatal(err)
	}
	index.Latest = "9.9.9"
	if _, err := verifier.VerifyReleaseIndex(mustJSON(t, index)); err == nil {
		t.Fatal("index with a forged latest was accepted")
	}

	index, err = BuildReleaseIndex(BuildReleaseIndexOptions{Entries: testIndexEntries(), Key: key, Cert: cert})
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(index.Releases)-1; i < j; i, j = i+1, j-1 {
		index.Releases[i], index.Releases[j] = index.Releases[j], index.Releases[i]
	}
	if _, err := verifier.VerifyReleaseIndex(mustJSON(t, index)); err == nil {
		t.Fatal("index with unsorted entries was accepted")
	}
}

func TestDetachedSignatureRoundTrip(t *testing.T) {
	_, key, cert, _ := testFundingKeys(t)
	data := []byte(`{"format":"toget-release-channel","version":1}`)
	signature, err := BuildDetachedSignature("channels/stable.json", data, key)
	if err != nil {
		t.Fatal(err)
	}
	if signature.Document != "channels/stable.json" || signature.SHA256 != hashHex(data) {
		t.Fatalf("signature = %+v", signature)
	}
	if err := VerifyDetachedSignature("channels/stable.json", data, cert, *signature); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDetachedSignature("channels/beta.json", data, cert, *signature); err == nil {
		t.Fatal("sidecar for another document was accepted")
	}
	tampered := append([]byte(nil), data...)
	tampered[0] = 'X'
	if err := VerifyDetachedSignature("channels/stable.json", tampered, cert, *signature); err == nil {
		t.Fatal("sidecar with a mismatched hash was accepted")
	}
	bad := *signature
	bad.Signature = strings.Repeat("A", 86)
	if err := VerifyDetachedSignature("channels/stable.json", data, cert, bad); err == nil {
		t.Fatal("sidecar with a bad signature was accepted")
	}
	if _, err := BuildDetachedSignature("../escape.json", data, key); err == nil {
		t.Fatal("sidecar accepted an unsafe document path")
	}
	if _, err := BuildDetachedSignature("channels/stable.json", nil, key); err == nil {
		t.Fatal("sidecar accepted an empty document")
	}
}
