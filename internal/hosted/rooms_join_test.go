package hosted

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestJoinSecretHash(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	sum := sha256.Sum256([]byte(secret))
	tests := []struct {
		name string
		req  RoomJoinRequest
		want []byte
		ok   bool
	}{
		{name: "hash", req: RoomJoinRequest{SecretHash: sum[:]}, want: sum[:], ok: true},
		{name: "legacy secret", req: RoomJoinRequest{Secret: secret}, want: sum[:], ok: true},
		{name: "missing", req: RoomJoinRequest{}, ok: false},
		{name: "short legacy secret", req: RoomJoinRequest{Secret: "short"}, ok: false},
		{name: "bad hash length", req: RoomJoinRequest{SecretHash: sum[:16]}, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := joinSecretHash(tc.req)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("hash = %x, want %x", got, tc.want)
			}
		})
	}
}
