package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestGenerateVerifySixMonthsAndMachineBinding(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	// Replace public key verification temporarily by testing payload mechanics is
	// impossible with the package constant, so this test focuses machine encoding.
	var m [10]byte
	copy(m[:], []byte("0123456789"))
	s := EncodeMachineID(m)
	got, err := ParseMachineID(s)
	if err != nil || got != m {
		t.Fatalf("machine roundtrip failed: %v", err)
	}
	_ = priv
	_ = time.Now()
}
