package hosted

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
)

func TestRoomJoinDecodeRequiresSecretHash(t *testing.T) {
	sum := sha256.Sum256([]byte("0123456789abcdef0123456789abcdef"))
	valid, err := json.Marshal(RoomJoinRequest{
		Protocol: Protocol, RoomID: "room", SecretHash: sum[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	var request RoomJoinRequest
	if err := decodeRoomBody(valid, &request); err != nil {
		t.Fatalf("decode secret_hash request: %v", err)
	}
	if len(request.SecretHash) != sha256.Size {
		t.Fatalf("secret_hash length = %d", len(request.SecretHash))
	}

	legacy := []byte(`{"protocol":1,"room_id":"room","secret":"0123456789abcdef0123456789abcdef","device":{}}`)
	if err := decodeRoomBody(legacy, &request); err == nil {
		t.Fatal("legacy raw-secret join request was accepted")
	}
}
