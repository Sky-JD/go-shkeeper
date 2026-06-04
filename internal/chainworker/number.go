package chainworker

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/shopspring/decimal"
)

func hexToInt(value string) (int64, error) {
	i := new(big.Int)
	if _, ok := i.SetString(strings.TrimPrefix(value, "0x"), 16); !ok {
		return 0, fmt.Errorf("invalid hex integer: %s", value)
	}
	return i.Int64(), nil
}

func hexToDecimal(value string) (decimal.Decimal, error) {
	i := new(big.Int)
	if _, ok := i.SetString(strings.TrimPrefix(value, "0x"), 16); !ok {
		return decimal.Zero, fmt.Errorf("invalid hex integer: %s", value)
	}
	return decimal.NewFromBigInt(i, 0), nil
}
