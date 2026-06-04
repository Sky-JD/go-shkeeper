package chainworker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	secpECDSA "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func recoverableSignature(privateKeyHex string, digest []byte) ([]byte, []byte, byte, error) {
	if len(digest) != 32 {
		return nil, nil, 0, errors.New("digest must be 32 bytes")
	}
	privateKeyBytes, err := hex.DecodeString(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return nil, nil, 0, err
	}
	privateKey := secp.PrivKeyFromBytes(privateKeyBytes)
	compact := secpECDSA.SignCompact(privateKey, digest, false)
	if len(compact) != 65 {
		return nil, nil, 0, fmt.Errorf("unexpected compact signature length: %d", len(compact))
	}
	recoveryID := compact[0] - 27
	r := append([]byte{}, compact[1:33]...)
	s := append([]byte{}, compact[33:65]...)
	return r, s, recoveryID, nil
}

func signLegacyEVMTransaction(privateKeyHex string, nonce uint64, gasPrice *big.Int, gasLimit uint64, to []byte, value *big.Int, data []byte, chainID *big.Int) ([]byte, []byte, error) {
	if len(to) != 20 {
		return nil, nil, fmt.Errorf("EVM destination must be 20 bytes")
	}
	if chainID == nil || chainID.Sign() <= 0 {
		return nil, nil, fmt.Errorf("EVM chain id must be positive")
	}
	signingPayload := rlpList(
		rlpUint(nonce),
		rlpBig(gasPrice),
		rlpUint(gasLimit),
		rlpBytes(to),
		rlpBig(value),
		rlpBytes(data),
		rlpBig(chainID),
		rlpBytes(nil),
		rlpBytes(nil),
	)
	digest := keccak256(signingPayload)
	r, s, recoveryID, err := recoverableSignature(privateKeyHex, digest)
	if err != nil {
		return nil, nil, err
	}
	v := new(big.Int).Mul(chainID, big.NewInt(2))
	v.Add(v, big.NewInt(35+int64(recoveryID)))
	raw := rlpList(
		rlpUint(nonce),
		rlpBig(gasPrice),
		rlpUint(gasLimit),
		rlpBytes(to),
		rlpBig(value),
		rlpBytes(data),
		rlpBig(v),
		rlpBytes(trimLeadingZeroes(r)),
		rlpBytes(trimLeadingZeroes(s)),
	)
	return raw, keccak256(raw), nil
}
