package account_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/vault"
)

type syncFixture struct {
	service  *account.Service
	snapshot *account.Snapshot
	account  ed25519.PrivateKey
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.LoadOrCreate(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(dir, "toget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service := account.New(db, v)
	snapshot, err := service.Create(context.Background(), "Aiden", "testpass123", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	key, err := service.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		t.Fatal(err)
	}
	return &syncFixture{service: service, snapshot: snapshot, account: key}
}

// makeDeviceRecord builds a valid record without storing it, as a paired
// device would learn it over the network.
func (f *syncFixture) makeDeviceRecord(t *testing.T, name, nodeURL string, issuedAt time.Time) account.DeviceRecord {
	t.Helper()
	devicePub, _, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptionPub, _, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	deviceID := identity.NewID()
	cert, err := identity.SignDeviceCertificate(deviceID, f.snapshot.Account.ID, devicePub, encryptionPub, f.account, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	return account.DeviceRecord{
		ID: deviceID, AccountID: f.snapshot.Account.ID, Name: name,
		PublicKey: devicePub, EncryptionPublicKey: encryptionPub,
		Certificate: string(cert), NodeURL: nodeURL, Role: "node",
		CreatedAt: issuedAt.UTC().Truncate(time.Millisecond),
	}
}

func TestExportDeviceRecordsUsesLiveAddressAndSignsRevocations(t *testing.T) {
	ctx := context.Background()
	f := newSyncFixture(t)
	record := f.makeDeviceRecord(t, "phone", "https://10.0.0.2:7717#fp="+strings.Repeat("a", 64), time.Now().UTC())
	if err := f.service.AddDevice(ctx, &account.Device{
		ID: record.ID, AccountID: record.AccountID, Name: record.Name,
		PublicKey: record.PublicKey, EncryptionPublicKey: record.EncryptionPublicKey,
		Certificate: record.Certificate, NodeURL: record.NodeURL, Role: record.Role,
		CreatedAt: record.CreatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RevokeDevice(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	live := "https://10.0.0.1:7717#fp=" + strings.Repeat("b", 64)
	records, err := f.service.ExportDeviceRecords(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("exported %d records, want 2", len(records))
	}
	for i := range records {
		switch records[i].ID {
		case f.snapshot.Device.ID:
			if records[i].NodeURL != live {
				t.Fatalf("local node URL = %q, want %q", records[i].NodeURL, live)
			}
		case record.ID:
			if records[i].Revocation == "" || records[i].RevokedAt == nil {
				t.Fatal("revoked device was exported without a signed revocation")
			}
			proof, err := identity.VerifyAccountRevocation(records[i].Revocation, ed25519.PublicKey(f.snapshot.Account.PublicKey))
			if err != nil {
				t.Fatal(err)
			}
			if proof.DeviceID != record.ID || !proof.RevokedAt.Equal(*records[i].RevokedAt) {
				t.Fatalf("unexpected revocation proof: %#v", proof)
			}
		default:
			t.Fatalf("unexpected device %s", records[i].ID)
		}
	}
}

func TestMergeDeviceRecordsAddsUpdatesAndNeverUnrevokes(t *testing.T) {
	ctx := context.Background()
	f := newSyncFixture(t)
	record := f.makeDeviceRecord(t, "phone", "https://10.0.0.2:7717#fp="+strings.Repeat("a", 64), time.Now().UTC())

	result, err := f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("merge result = %+v", result)
	}
	stored, err := f.service.GetDeviceByID(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NodeURL != record.NodeURL || stored.Local {
		t.Fatalf("stored device = %#v", stored)
	}

	// A fresher certificate, renamed device, and new address update in place.
	updated := record
	updated.Name = "Phone (new)"
	updated.NodeURL = "https://10.0.0.3:7717#fp=" + strings.Repeat("c", 64)
	cert, err := identity.SignDeviceCertificate(record.ID, record.AccountID,
		ed25519.PublicKey(record.PublicKey), record.EncryptionPublicKey, f.account, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	updated.Certificate = string(cert)
	result, err = f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{updated})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Added != 0 {
		t.Fatalf("update result = %+v", result)
	}
	stored, err = f.service.GetDeviceByID(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != updated.Name || stored.NodeURL != updated.NodeURL || stored.Certificate != updated.Certificate {
		t.Fatalf("device was not updated: %#v", stored)
	}

	// Revocation requires a signed statement and is permanent.
	revokedAt := time.Now().UTC().Truncate(time.Millisecond)
	proof, err := identity.SignAccountRevocation(record.AccountID, record.ID, revokedAt, f.account)
	if err != nil {
		t.Fatal(err)
	}
	updated.Revocation = proof
	updated.RevokedAt = &revokedAt
	result, err = f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{updated})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revoked != 1 || len(result.RevokedIDs) != 1 || result.RevokedIDs[0] != record.ID {
		t.Fatalf("revocation result = %+v", result)
	}
	active := updated
	active.RevokedAt = nil
	active.Revocation = ""
	active.Name = "Resurrected"
	result, err = f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{active})
	if err != nil {
		t.Fatal(err)
	}
	stored, err = f.service.GetDeviceByID(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RevokedAt == nil || stored.Name == "Resurrected" {
		t.Fatalf("an un-revocation was applied: %#v", stored)
	}
	if result.Updated != 0 || result.Revoked != 0 {
		t.Fatalf("active record changed a revoked device: %+v", result)
	}

	// A revocation without its signature is rejected outright.
	unsigned := f.makeDeviceRecord(t, "tablet", "", time.Now().UTC())
	unsigned.RevokedAt = &revokedAt
	result, err = f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{unsigned})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Skipped != 1 || len(result.Warnings) != 1 {
		t.Fatalf("unsigned revocation result = %+v", result)
	}
}

func TestMergeDeviceRecordsSkipsBadRecordsButKeepsGoodOnes(t *testing.T) {
	ctx := context.Background()
	f := newSyncFixture(t)
	good := f.makeDeviceRecord(t, "good", "", time.Now().UTC())
	badKey := f.makeDeviceRecord(t, "bad-key", "", time.Now().UTC())
	badKey.PublicKey = append([]byte(nil), badKey.PublicKey...)
	badKey.PublicKey[0] ^= 0xff
	badCert := f.makeDeviceRecord(t, "bad-cert", "", time.Now().UTC())
	badCert.Certificate = strings.Replace(badCert.Certificate, `"device_id":"`+badCert.ID+`"`, `"device_id":"`+identity.NewID()+`"`, 1)
	badURL := f.makeDeviceRecord(t, "bad-url", "http://example.test:7717", time.Now().UTC())
	otherAccount := f.makeDeviceRecord(t, "other-account", "", time.Now().UTC())
	otherAccount.AccountID = "00000000000000"
	duplicate := good

	result, err := f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{good, badKey, badCert, badURL, otherAccount, duplicate})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Skipped != 5 {
		t.Fatalf("merge result = %+v", result)
	}
	if _, err := f.service.GetDeviceByID(ctx, good.ID); err != nil {
		t.Fatalf("good device was not merged: %v", err)
	}
	if _, err := f.service.GetDeviceByID(ctx, badKey.ID); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("bad-key device was merged: %v", err)
	}
}

func TestMergeDeviceRecordsAcceptsExpiredButSignedCertificates(t *testing.T) {
	ctx := context.Background()
	f := newSyncFixture(t)
	// A device offline for over a year still needs to sync a renewed cert.
	record := f.makeDeviceRecord(t, "old-laptop", "", time.Now().UTC().AddDate(-2, 0, 0))
	result, err := f.service.MergeDeviceRecords(ctx, []account.DeviceRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 {
		t.Fatalf("merge result = %+v", result)
	}
}

func TestMergeDeviceRecordsRejectsTooMany(t *testing.T) {
	f := newSyncFixture(t)
	tooMany := make([]account.DeviceRecord, account.MaxDeviceRecords+1)
	if _, err := f.service.MergeDeviceRecords(context.Background(), tooMany); !errors.Is(err, account.ErrTooManyDeviceRecords) {
		t.Fatalf("merge error = %v", err)
	}
}

func TestSetHostURLAcceptsIdentityPinAndStaysImmutable(t *testing.T) {
	ctx := context.Background()
	f := newSyncFixture(t)
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	hostURL := "https://accounts.example.com#id=" + server.ID
	if err := f.service.SetHostURL(ctx, hostURL); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Account.HostURL != hostURL {
		t.Fatalf("host URL = %q", snapshot.Account.HostURL)
	}
	other, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetHostURL(ctx, "https://accounts.example.com#id="+other.ID); !errors.Is(err, account.ErrHostImmutable) {
		t.Fatalf("second host error = %v", err)
	}
	if err := f.service.SetHostURL(ctx, "https://accounts.example.com#id=bad"); !errors.Is(err, account.ErrInvalidHostURL) {
		t.Fatalf("invalid host error = %v", err)
	}
}

func TestSetDeviceNodeURLValidates(t *testing.T) {
	ctx := context.Background()
	f := newSyncFixture(t)
	valid := "https://10.0.0.9:7717#fp=" + strings.Repeat("d", 64)
	if err := f.service.SetDeviceNodeURL(ctx, f.snapshot.Device.ID, valid); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Device.NodeURL != valid {
		t.Fatalf("node URL = %q", snapshot.Device.NodeURL)
	}
	for _, bad := range []string{
		"http://example.test#fp=" + strings.Repeat("d", 64),
		"https://user@example.test#fp=" + strings.Repeat("d", 64),
		"https://example.test",
		"https://example.test#fp=not-a-fingerprint",
		"https://example.test/?q=1#fp=" + strings.Repeat("d", 64),
		"https://" + strings.Repeat("x", account.MaxNodeURLLength),
	} {
		if err := f.service.SetDeviceNodeURL(ctx, f.snapshot.Device.ID, bad); !errors.Is(err, account.ErrInvalidDeviceRecord) {
			t.Fatalf("SetDeviceNodeURL(%q) = %v", bad, err)
		}
	}
}
