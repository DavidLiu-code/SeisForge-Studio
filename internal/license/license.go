package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	payloadMagic   = "L1IC"
	payloadVersion = byte(1)
	machineBytes   = 10
)

// PublicKeyBase64 is safe to distribute with SeisForge Studio. The matching private key
// must remain only in the owner's administrator package.
const PublicKeyBase64 = "tJCkXQLlSLTrNURPa4hkKyckXxAKiX1MUZerpgyJmCM"

type Info struct {
	Licensee string
	Issued   time.Time
	Expires  time.Time
	Serial   string
	Machine  string
}

func PublicKey() ed25519.PublicKey {
	b, _ := base64.RawStdEncoding.DecodeString(PublicKeyBase64)
	return ed25519.PublicKey(b)
}

func EncodeMachineID(id [machineBytes]byte) string {
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(id[:])
	var parts []string
	for len(s) > 4 {
		parts = append(parts, s[:4])
		s = s[4:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "-")
}

func ParseMachineID(s string) ([machineBytes]byte, error) {
	var out [machineBytes]byte
	clean := strings.ToUpper(strings.TrimSpace(s))
	clean = strings.NewReplacer("-", "", " ", "", "\r", "", "\n", "", "\t", "").Replace(clean)
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(clean)
	if err != nil || len(b) != machineBytes {
		return out, errors.New("invalid SeisForge Studio machine code")
	}
	copy(out[:], b)
	return out, nil
}

func dayNumber(t time.Time) uint32 {
	return uint32(t.UTC().Unix() / 86400)
}

func dayTime(d uint32) time.Time {
	return time.Unix(int64(d)*86400, 0).UTC()
}

func Generate(privateKey ed25519.PrivateKey, machine [machineBytes]byte, licensee string, now time.Time) (string, Info, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", Info{}, errors.New("invalid Ed25519 private key")
	}
	licensee = strings.TrimSpace(licensee)
	if licensee == "" {
		licensee = "Licensed User"
	}
	if len([]byte(licensee)) > 80 {
		return "", Info{}, errors.New("licensee name too long")
	}
	issuedDay := dayNumber(now)
	expires := now.UTC().AddDate(0, 6, 0)
	expiresDay := dayNumber(expires)
	serial := make([]byte, 8)
	if _, err := rand.Read(serial); err != nil {
		return "", Info{}, err
	}
	name := []byte(licensee)
	payload := make([]byte, 0, 4+1+4+4+machineBytes+8+1+len(name))
	payload = append(payload, []byte(payloadMagic)...)
	payload = append(payload, payloadVersion)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, issuedDay)
	payload = append(payload, tmp...)
	binary.BigEndian.PutUint32(tmp, expiresDay)
	payload = append(payload, tmp...)
	payload = append(payload, machine[:]...)
	payload = append(payload, serial...)
	payload = append(payload, byte(len(name)))
	payload = append(payload, name...)
	sig := ed25519.Sign(privateKey, payload)
	code := "L1-" + base64.RawURLEncoding.EncodeToString(append(payload, sig...))
	info := Info{Licensee: licensee, Issued: dayTime(issuedDay), Expires: dayTime(expiresDay), Serial: fmt.Sprintf("%X", serial), Machine: EncodeMachineID(machine)}
	return code, info, nil
}

func Verify(code string, machine [machineBytes]byte, now time.Time) (Info, error) {
	clean := strings.TrimSpace(code)
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, " ", "")
	if !strings.HasPrefix(clean, "L1-") {
		return Info{}, errors.New("registration code prefix is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(clean, "L1-"))
	if err != nil {
		return Info{}, errors.New("registration code encoding is invalid")
	}
	minPayload := 4 + 1 + 4 + 4 + machineBytes + 8 + 1
	if len(raw) < minPayload+ed25519.SignatureSize {
		return Info{}, errors.New("registration code is too short")
	}
	payload := raw[:len(raw)-ed25519.SignatureSize]
	sig := raw[len(raw)-ed25519.SignatureSize:]
	if !ed25519.Verify(PublicKey(), payload, sig) {
		return Info{}, errors.New("registration code signature is invalid")
	}
	if string(payload[:4]) != payloadMagic || payload[4] != payloadVersion {
		return Info{}, errors.New("registration code version is unsupported")
	}
	off := 5
	issuedDay := binary.BigEndian.Uint32(payload[off : off+4])
	off += 4
	expiresDay := binary.BigEndian.Uint32(payload[off : off+4])
	off += 4
	var licMachine [machineBytes]byte
	copy(licMachine[:], payload[off:off+machineBytes])
	off += machineBytes
	if licMachine != machine {
		return Info{}, errors.New("registration code belongs to another computer")
	}
	serial := payload[off : off+8]
	off += 8
	if off >= len(payload) {
		return Info{}, errors.New("registration payload is truncated")
	}
	n := int(payload[off])
	off++
	if off+n != len(payload) {
		return Info{}, errors.New("registration payload length is invalid")
	}
	name := string(payload[off : off+n])
	today := dayNumber(now)
	if today < issuedDay {
		return Info{}, errors.New("system date is earlier than license issue date")
	}
	if today > expiresDay {
		return Info{}, errors.New("registration code has expired")
	}
	return Info{Licensee: name, Issued: dayTime(issuedDay), Expires: dayTime(expiresDay), Serial: fmt.Sprintf("%X", serial), Machine: EncodeMachineID(machine)}, nil
}
