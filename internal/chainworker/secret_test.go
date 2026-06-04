package chainworker

import "testing"

func TestEncryptSecretRoundTrip(t *testing.T) {
	encrypted, err := encryptSecret("account-password", "private-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if encrypted == "private-key" {
		t.Fatalf("secret was not encrypted")
	}
	decrypted, err := decryptSecret("account-password", encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != "private-key" {
		t.Fatalf("unexpected decrypted value: %s", decrypted)
	}
}
