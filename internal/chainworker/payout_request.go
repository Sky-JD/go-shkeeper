package chainworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

type payoutRequest struct {
	Destination    string
	DestinationTag string
	Amount         decimal.Decimal
	Fee            string
	ExternalID     string
}

func parsePayoutRequests(body []byte) ([]payoutRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, errors.New("expected an array of payouts")
	}
	if len(rows) == 0 {
		return nil, errors.New("at least one payout is required")
	}
	out := make([]payoutRequest, 0, len(rows))
	for i, row := range rows {
		destination := strings.TrimSpace(stringFromAny(firstValue(row, "destination", "dest", "address", "to_address")))
		amount, ok := decimalFromAny(firstValue(row, "amount", "amount_crypto"))
		if destination == "" || !ok || !amount.GreaterThan(decimal.Zero) {
			return nil, fmt.Errorf("payout %d requires destination and positive amount", i)
		}
		out = append(out, payoutRequest{
			Destination:    destination,
			DestinationTag: strings.TrimSpace(stringFromAny(firstValue(row, "dest_tag", "destination_tag", "tag"))),
			Amount:         amount,
			Fee:            strings.TrimSpace(stringFromAny(firstValue(row, "fee"))),
			ExternalID:     strings.TrimSpace(stringFromAny(firstValue(row, "external_id"))),
		})
	}
	return out, nil
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return string(x)
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func decimalFromAny(v any) (decimal.Decimal, bool) {
	switch x := v.(type) {
	case nil:
		return decimal.Zero, false
	case decimal.Decimal:
		return x, true
	case json.Number:
		d, err := decimal.NewFromString(string(x))
		return d, err == nil
	case string:
		d, err := decimal.NewFromString(strings.TrimSpace(x))
		return d, err == nil
	case float64:
		return decimal.NewFromFloat(x), true
	case int:
		return decimal.NewFromInt(int64(x)), true
	case int64:
		return decimal.NewFromInt(x), true
	default:
		return decimal.Zero, false
	}
}
