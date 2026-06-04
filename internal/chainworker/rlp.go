package chainworker

import "math/big"

func rlpBytes(value []byte) []byte {
	if len(value) == 1 && value[0] < 0x80 {
		return append([]byte{}, value...)
	}
	return append(rlpLengthPrefix(0x80, len(value)), value...)
}

func rlpUint(value uint64) []byte {
	if value == 0 {
		return rlpBytes(nil)
	}
	var buf [8]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte(value)
		value >>= 8
	}
	return rlpBytes(buf[i:])
}

func rlpBig(value *big.Int) []byte {
	if value == nil || value.Sign() == 0 {
		return rlpBytes(nil)
	}
	return rlpBytes(trimLeadingZeroes(value.Bytes()))
}

func rlpList(items ...[]byte) []byte {
	length := 0
	for _, item := range items {
		length += len(item)
	}
	out := rlpLengthPrefix(0xc0, length)
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

func rlpLengthPrefix(offset byte, length int) []byte {
	if length < 56 {
		return []byte{offset + byte(length)}
	}
	lengthBytes := intBytes(length)
	out := []byte{offset + 55 + byte(len(lengthBytes))}
	out = append(out, lengthBytes...)
	return out
}

func intBytes(value int) []byte {
	if value == 0 {
		return nil
	}
	var buf [8]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte(value)
		value >>= 8
	}
	return buf[i:]
}

func trimLeadingZeroes(value []byte) []byte {
	for len(value) > 0 && value[0] == 0 {
		value = value[1:]
	}
	return value
}
