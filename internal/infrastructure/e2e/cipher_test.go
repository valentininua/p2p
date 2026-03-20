package e2e

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := DeriveRoomKey("super-secret")

	encrypted, err := Encrypt([]byte("hello"), key)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if got := string(decrypted); got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
}

func TestDecryptFailsWithWrongKey(t *testing.T) {
	encrypted, err := Encrypt([]byte("hello"), DeriveRoomKey("first"))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if _, err := Decrypt(encrypted, DeriveRoomKey("second")); err == nil {
		t.Fatal("expected decrypt to fail with wrong key")
	}
}
