package hosted_test

import (
	"bytes"
	"testing"

	"github.com/sidenor/toget/internal/hosted"
)

const testHostID = "srvs-testhost-0123456789abcdef"

func opaqueSetup(t *testing.T) (*hosted.OPAQUEHost, []byte) {
	t.Helper()
	conf, configBytes, err := hosted.NewOPAQUEConfig()
	if err != nil {
		t.Fatal(err)
	}
	material, err := hosted.NewOPAQUEMaterial(conf, testHostID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := hosted.NewOPAQUEHost(configBytes, material)
	if err != nil {
		t.Fatal(err)
	}
	return server, configBytes
}

func register(t *testing.T, server *hosted.OPAQUEHost, configBytes []byte, accountID, password string) ([]byte, []byte) {
	t.Helper()
	client, err := hosted.NewOPAQUEClient(configBytes, accountID, testHostID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.RegisterInit(password)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.RegistrationResponse(accountID, request)
	if err != nil {
		t.Fatal(err)
	}
	record, exportKey, err := client.RegisterFinish(response)
	if err != nil {
		t.Fatal(err)
	}
	return record, exportKey
}

func TestOPAQUERegistrationAndLogin(t *testing.T) {
	server, configBytes := opaqueSetup(t)
	recordBytes, registerExport := register(t, server, configBytes, testAccount, "correct horse battery staple")
	clientRecord, err := server.RegistrationRecord(testAccount, recordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(clientRecord.CredentialIdentifier) != testAccount || string(clientRecord.ClientIdentity) != testAccount {
		t.Fatalf("record identity = %q / %q", clientRecord.CredentialIdentifier, clientRecord.ClientIdentity)
	}
	encoded := server.EncodeRecord(clientRecord)
	stored, err := server.DecodeRecord(testAccount, encoded)
	if err != nil {
		t.Fatal(err)
	}

	client, err := hosted.NewOPAQUEClient(configBytes, testAccount, testHostID)
	if err != nil {
		t.Fatal(err)
	}
	ke1, err := client.LoginInit("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := server.LoginBegin(testAccount, stored, ke1)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Fake {
		t.Fatal("a known account used the fake path")
	}
	ke3, loginExport, err := client.LoginFinish(enrollment.KE2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(registerExport, loginExport) {
		t.Fatal("registration and login export keys differ")
	}
	if err := server.LoginFinish(enrollment.Output, enrollment.Fake, ke3); err != nil {
		t.Fatalf("login finish: %v", err)
	}
}

func TestOPAQUEWrongPasswordFails(t *testing.T) {
	server, configBytes := opaqueSetup(t)
	recordBytes, _ := register(t, server, configBytes, testAccount, "right password")
	clientRecord, err := server.RegistrationRecord(testAccount, recordBytes)
	if err != nil {
		t.Fatal(err)
	}
	client, err := hosted.NewOPAQUEClient(configBytes, testAccount, testHostID)
	if err != nil {
		t.Fatal(err)
	}
	ke1, err := client.LoginInit("wrong password")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := server.LoginBegin(testAccount, clientRecord, ke1)
	if err != nil {
		t.Fatal(err)
	}
	ke3, _, err := client.LoginFinish(enrollment.KE2)
	if err == nil {
		if err := server.LoginFinish(enrollment.Output, enrollment.Fake, ke3); err == nil {
			t.Fatal("wrong password completed login")
		}
	}
}

func TestOPAQUEUnknownAccountUsesFakeRecord(t *testing.T) {
	server, configBytes := opaqueSetup(t)
	client, err := hosted.NewOPAQUEClient(configBytes, testAccount, testHostID)
	if err != nil {
		t.Fatal(err)
	}
	ke1, err := client.LoginInit("some password")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := server.LoginBegin(testAccount, nil, ke1)
	if err != nil {
		t.Fatal(err)
	}
	if !enrollment.Fake {
		t.Fatal("unknown account did not use the fake path")
	}
	ke3, _, err := client.LoginFinish(enrollment.KE2)
	if err == nil {
		if err := server.LoginFinish(enrollment.Output, enrollment.Fake, ke3); err == nil {
			t.Fatal("fake enrollment completed login")
		}
	}
}

func TestOPAQUERegistrationRecordIsBoundToAccount(t *testing.T) {
	server, configBytes := opaqueSetup(t)
	recordBytes, _ := register(t, server, configBytes, testAccount, "password")

	// Re-labeling a registration record with another account ID must not make
	// that account's login succeed. The OPAQUE envelope binds the credentialed
	// identity even though the host store indexes records by account ID.
	const otherAccount = "12345678000002"
	clientRecord, err := server.RegistrationRecord(otherAccount, recordBytes)
	if err != nil {
		t.Fatalf("record could not be decoded: %v", err)
	}
	client, err := hosted.NewOPAQUEClient(configBytes, otherAccount, testHostID)
	if err != nil {
		t.Fatal(err)
	}
	ke1, err := client.LoginInit("password")
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := server.LoginBegin(otherAccount, clientRecord, ke1)
	if err != nil {
		return // Rejecting the mismatched record up front is also correct.
	}
	ke3, _, err := client.LoginFinish(enrollment.KE2)
	if err != nil {
		return
	}
	if err := server.LoginFinish(enrollment.Output, enrollment.Fake, ke3); err == nil {
		t.Fatal("a record registered under another account completed login")
	}
}
