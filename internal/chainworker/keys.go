package chainworker

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func newBNBAccount() (address string, privateKeyHex string, err error) {
	priv, err := secp.GeneratePrivateKey()
	if err != nil {
		return "", "", err
	}
	hash := keccak256(priv.PubKey().SerializeUncompressed()[1:])
	return "0x" + hex.EncodeToString(hash[12:]), hex.EncodeToString(priv.Serialize()), nil
}

func newTRONAccount() (address string, privateKeyHex string, err error) {
	priv, err := secp.GeneratePrivateKey()
	if err != nil {
		return "", "", err
	}
	hash := keccak256(priv.PubKey().SerializeUncompressed()[1:])
	payload := append([]byte{0x41}, hash[12:]...)
	return base58Check(payload), hex.EncodeToString(priv.Serialize()), nil
}

func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func base58Check(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	full := append(append([]byte{}, payload...), second[:4]...)
	return base58Encode(full)
}

func base58Encode(data []byte) string {
	x := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58CheckDecode(value string) ([]byte, error) {
	decoded, err := base58Decode(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 5 {
		return nil, errors.New("base58check value is too short")
	}
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(checksum, second[:4]) {
		return nil, errors.New("base58check checksum mismatch")
	}
	return payload, nil
}

func base58Decode(value string) ([]byte, error) {
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range value {
		index := int64(-1)
		for i, b := range base58Alphabet {
			if r == b {
				index = int64(i)
				break
			}
		}
		if index < 0 {
			return nil, errors.New("invalid base58 character")
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(index))
	}
	out := x.Bytes()
	leading := 0
	for _, r := range value {
		if r != rune(base58Alphabet[0]) {
			break
		}
		leading++
	}
	if leading > 0 {
		out = append(make([]byte, leading), out...)
	}
	return out, nil
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
