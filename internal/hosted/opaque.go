package hosted

import (
	"errors"
	"fmt"

	"github.com/bytemare/opaque"
)

// opaqueContext separates Toget account logins from any other use of the same
// OPAQUE library.
const opaqueContext = "toget-hosted-opaque-v1"

// NewOPAQUEConfig returns the v1 configuration and its serialized form.
func NewOPAQUEConfig() (*opaque.Configuration, []byte, error) {
	conf := opaque.DefaultConfiguration()
	conf.Context = []byte(opaqueContext)
	data := conf.Serialize()
	decoded, err := opaque.DeserializeConfiguration(data)
	if err != nil {
		return nil, nil, fmt.Errorf("opaque configuration: %w", err)
	}
	if err := conf.Equals(decoded); err != nil {
		return nil, nil, fmt.Errorf("opaque configuration round trip: %w", err)
	}
	return decoded, data, nil
}

// NewOPAQUEMaterial creates the host-wide OPAQUE key material. It must be
// backed up with the host identity; losing it makes every account unable to
// log in with a password (recovery still needs the host record).
func NewOPAQUEMaterial(conf *opaque.Configuration, hostID string) ([]byte, error) {
	if conf == nil || hostID == "" {
		return nil, errors.New("opaque setup is incomplete")
	}
	privateKey, publicKey := conf.KeyGen()
	seed := conf.GenerateOPRFSeed()
	material := &opaque.ServerKeyMaterial{
		Identity:       []byte(hostID),
		PrivateKey:     privateKey,
		PublicKeyBytes: publicKey.Encode(),
		OPRFGlobalSeed: seed,
	}
	encoded := material.Encode()
	material.Flush()
	return encoded, nil
}

// OPAQUEHost wraps the server side of registration and login.
type OPAQUEHost struct {
	conf   *opaque.Configuration
	server *opaque.Server
}

// NewOPAQUEHost loads host key material and prepares the OPAQUE server.
func NewOPAQUEHost(configBytes, materialBytes []byte) (*OPAQUEHost, error) {
	conf, err := opaque.DeserializeConfiguration(configBytes)
	if err != nil {
		return nil, fmt.Errorf("opaque configuration: %w", err)
	}
	current, _, err := NewOPAQUEConfig()
	if err != nil {
		return nil, err
	}
	if err := conf.Equals(current); err != nil {
		return nil, fmt.Errorf("opaque configuration is not the v1 suite: %w", err)
	}
	material, err := conf.DecodeServerKeyMaterial(materialBytes)
	if err != nil {
		return nil, fmt.Errorf("opaque key material: %w", err)
	}
	server, err := conf.Server()
	if err != nil {
		return nil, fmt.Errorf("opaque server: %w", err)
	}
	if err := server.SetKeyMaterial(material); err != nil {
		return nil, fmt.Errorf("opaque key material: %w", err)
	}
	return &OPAQUEHost{conf: conf, server: server}, nil
}

// RegistrationResponse starts OPAQUE registration for a new account.
func (h *OPAQUEHost) RegistrationResponse(accountID string, request []byte) ([]byte, error) {
	if h == nil || !digits(accountID, 14) || len(request) == 0 || len(request) > MaxOpaqueRequest {
		return nil, errors.New("invalid registration request")
	}
	req, err := h.server.Deserialize.RegistrationRequest(request)
	if err != nil {
		return nil, err
	}
	response, err := h.server.RegistrationResponse(req, []byte(accountID), nil)
	if err != nil {
		return nil, err
	}
	return response.Serialize(), nil
}

// RegistrationRecord validates the final client record and wraps it for
// storage. The record is the client's proof that it completed registration.
func (h *OPAQUEHost) RegistrationRecord(accountID string, record []byte) (*opaque.ClientRecord, error) {
	if h == nil || !digits(accountID, 14) || len(record) == 0 || len(record) > MaxOpaqueRequest {
		return nil, errors.New("invalid registration record")
	}
	parsed, err := h.server.Deserialize.RegistrationRecord(record)
	if err != nil {
		return nil, err
	}
	return &opaque.ClientRecord{
		CredentialIdentifier: []byte(accountID),
		ClientIdentity:       []byte(accountID),
		RegistrationRecord:   parsed,
	}, nil
}

// Enrollment is one in-flight OPAQUE login. Output is the server MAC state
// that must be kept until the client sends KE3.
type Enrollment struct {
	KE2    []byte
	Output *opaque.ServerOutput
	Fake   bool
}

// LoginBegin computes KE2 from a stored record, or from a fake record when the
// account does not exist. The fake path keeps timing and response shape close
// to a real account and cannot complete login.
func (h *OPAQUEHost) LoginBegin(accountID string, record *opaque.ClientRecord, ke1 []byte) (*Enrollment, error) {
	if h == nil || !digits(accountID, 14) || len(ke1) == 0 || len(ke1) > MaxOpaqueRequest {
		return nil, errors.New("invalid login request")
	}
	fake := false
	if record == nil {
		var err error
		record, err = h.conf.GetFakeRecord([]byte(accountID))
		if err != nil {
			return nil, err
		}
		fake = true
	}
	message, err := h.server.Deserialize.KE1(ke1)
	if err != nil {
		return nil, err
	}
	ke2, output, err := h.server.GenerateKE2(message, record)
	if err != nil {
		return nil, err
	}
	return &Enrollment{KE2: ke2.Serialize(), Output: output, Fake: fake}, nil
}

// LoginFinish verifies KE3. A fake enrollment always fails.
func (h *OPAQUEHost) LoginFinish(output *opaque.ServerOutput, fake bool, ke3 []byte) error {
	if h == nil || output == nil || fake || len(ke3) == 0 || len(ke3) > MaxOpaqueRequest {
		return errors.New("invalid login")
	}
	message, err := h.server.Deserialize.KE3(ke3)
	if err != nil {
		return err
	}
	if err := h.server.LoginFinish(message, output.ClientMAC); err != nil {
		return err
	}
	return nil
}

// DecodeRecord parses a stored OPAQUE record.
func (h *OPAQUEHost) DecodeRecord(accountID string, encoded []byte) (*opaque.ClientRecord, error) {
	if h == nil || !digits(accountID, 14) || len(encoded) == 0 || len(encoded) > MaxOpaqueRequest {
		return nil, errors.New("invalid opaque record")
	}
	parsed, err := h.server.Deserialize.RegistrationRecord(encoded)
	if err != nil {
		return nil, err
	}
	return &opaque.ClientRecord{
		CredentialIdentifier: []byte(accountID),
		ClientIdentity:       []byte(accountID),
		RegistrationRecord:   parsed,
	}, nil
}

// EncodeRecord serializes a client record for storage. The credential and
// client identity are stable account metadata and do not need to be stored
// separately.
func (h *OPAQUEHost) EncodeRecord(record *opaque.ClientRecord) []byte {
	if record == nil || record.RegistrationRecord == nil {
		return nil
	}
	return record.RegistrationRecord.Serialize()
}

// OPAQUEClient performs the client half of registration and login.
type OPAQUEClient struct {
	client    *opaque.Client
	accountID string
	hostID    string
}

// NewOPAQUEClient creates a client with the host's advertised configuration.
func NewOPAQUEClient(configBytes []byte, accountID, hostID string) (*OPAQUEClient, error) {
	if !digits(accountID, 14) || hostID == "" {
		return nil, errors.New("invalid opaque client identity")
	}
	return newOPAQUEClient(configBytes, accountID, hostID)
}

// NewOPAQUERegistrationClient creates a client before registration has been
// assigned an account ID. Call RegisterFinishFor once the host responds.
func NewOPAQUERegistrationClient(configBytes []byte, hostID string) (*OPAQUEClient, error) {
	if hostID == "" {
		return nil, errors.New("invalid opaque client identity")
	}
	return newOPAQUEClient(configBytes, "", hostID)
}

func newOPAQUEClient(configBytes []byte, accountID, hostID string) (*OPAQUEClient, error) {
	conf, err := opaque.DeserializeConfiguration(configBytes)
	if err != nil {
		return nil, fmt.Errorf("opaque configuration: %w", err)
	}
	client, err := conf.Client()
	if err != nil {
		return nil, err
	}
	return &OPAQUEClient{client: client, accountID: accountID, hostID: hostID}, nil
}

// ValidOPAQUEConfig checks that a host uses the v1 Toget OPAQUE suite.
func ValidOPAQUEConfig(configBytes []byte) error {
	conf, err := opaque.DeserializeConfiguration(configBytes)
	if err != nil {
		return fmt.Errorf("opaque configuration: %w", err)
	}
	current, _, err := NewOPAQUEConfig()
	if err != nil {
		return err
	}
	if err := conf.Equals(current); err != nil {
		return fmt.Errorf("opaque configuration is not the v1 suite: %w", err)
	}
	return nil
}

// RegisterInit builds the first registration message.
func (c *OPAQUEClient) RegisterInit(password string) ([]byte, error) {
	if c == nil || password == "" {
		return nil, errors.New("password is required")
	}
	request, err := c.client.RegistrationInit([]byte(password))
	if err != nil {
		return nil, err
	}
	return request.Serialize(), nil
}

// RegisterFinish verifies the server response and returns the client record
// and export key. The export key wraps MK on the host.
func (c *OPAQUEClient) RegisterFinish(response []byte) (record, exportKey []byte, err error) {
	if c == nil || !digits(c.accountID, 14) {
		return nil, nil, errors.New("registration has no account id")
	}
	return c.RegisterFinishFor(c.accountID, response)
}

// RegisterFinishFor completes registration with the account ID assigned by
// the host.
func (c *OPAQUEClient) RegisterFinishFor(accountID string, response []byte) (record, exportKey []byte, err error) {
	if c == nil || !digits(accountID, 14) || len(response) == 0 {
		return nil, nil, errors.New("invalid registration response")
	}
	defer c.client.ClearState()
	message, err := c.client.Deserialize.RegistrationResponse(response)
	if err != nil {
		return nil, nil, err
	}
	recordMessage, exportKey, err := c.client.RegistrationFinalize(message, []byte(accountID), []byte(c.hostID))
	if err != nil {
		return nil, nil, err
	}
	return recordMessage.Serialize(), exportKey, nil
}

// LoginInit builds the first login message.
func (c *OPAQUEClient) LoginInit(password string) ([]byte, error) {
	if c == nil || password == "" {
		return nil, errors.New("password is required")
	}
	message, err := c.client.GenerateKE1([]byte(password))
	if err != nil {
		return nil, err
	}
	return message.Serialize(), nil
}

// LoginFinish verifies the server and returns KE3 and the export key.
func (c *OPAQUEClient) LoginFinish(response []byte) (ke3, exportKey []byte, err error) {
	if c == nil || len(response) == 0 {
		return nil, nil, errors.New("invalid login response")
	}
	defer c.client.ClearState()
	message, err := c.client.Deserialize.KE2(response)
	if err != nil {
		return nil, nil, err
	}
	ke3Message, _, exportKey, err := c.client.GenerateKE3(message, []byte(c.accountID), []byte(c.hostID))
	if err != nil {
		return nil, nil, err
	}
	return ke3Message.Serialize(), exportKey, nil
}

// Clear releases secret client state.
func (c *OPAQUEClient) Clear() {
	if c != nil && c.client != nil {
		c.client.ClearState()
	}
}
