package sealbox

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

func newKey(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key.PublicKey().Bytes(), key.Bytes()
}

func TestSealRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	plaintext := []byte("account private key material")
	sealed, err := Seal(pub, plaintext, "pairing|one|two")
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) == string(plaintext) {
		t.Fatal("sealed payload contains plaintext")
	}
	opened, err := Open(priv, sealed, "pairing|one|two")
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != string(plaintext) {
		t.Fatalf("opened = %q", opened)
	}
}

func TestSealRejectsWrongKeysAndContext(t *testing.T) {
	pub, priv := newKey(t)
	otherPub, otherPriv := newKey(t)
	sealed, err := Seal(pub, []byte("secret"), "context")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(otherPriv, sealed, "context"); err == nil {
		t.Fatal("wrong recipient key opened the payload")
	}
	if _, err := Open(priv, sealed, "other context"); err == nil {
		t.Fatal("wrong context opened the payload")
	}
	if _, err := Seal(otherPub, []byte("secret"), " "); err == nil {
		t.Fatal("empty context was accepted")
	}
}

func TestSealDetectsTampering(t *testing.T) {
	pub, priv := newKey(t)
	sealed, err := Seal(pub, []byte("secret"), "context")
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-2] ^= 1
	if _, err := Open(priv, sealed, "context"); err == nil {
		t.Fatal("tampered payload opened")
	}
}
