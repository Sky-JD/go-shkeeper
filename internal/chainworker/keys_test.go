package chainworker

import (
	"strings"
	"testing"
)

func TestNewBNBAccount(t *testing.T) {
	address, privateKey, err := newBNBAccount()
	if err != nil {
		t.Fatalf("new bnb account: %v", err)
	}
	if !strings.HasPrefix(address, "0x") || len(address) != 42 {
		t.Fatalf("unexpected bnb address: %s", address)
	}
	if len(privateKey) != 64 {
		t.Fatalf("unexpected private key length: %d", len(privateKey))
	}
}

func TestNewTRONAccount(t *testing.T) {
	address, privateKey, err := newTRONAccount()
	if err != nil {
		t.Fatalf("new tron account: %v", err)
	}
	if !strings.HasPrefix(address, "T") || len(address) < 30 {
		t.Fatalf("unexpected tron address: %s", address)
	}
	if len(privateKey) != 64 {
		t.Fatalf("unexpected private key length: %d", len(privateKey))
	}
}
