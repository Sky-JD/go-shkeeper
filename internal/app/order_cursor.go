package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var errInvalidOrderCursor = errors.New("invalid order cursor")

type orderCursorPayload struct {
	SortAt     string `json:"sort_at"`
	ExternalID string `json:"external_id"`
}

func encodeOrderCursor(row OrderCursorRow) string {
	if row.ExternalID == "" || row.SortAt.IsZero() {
		return ""
	}
	data, err := json.Marshal(orderCursorPayload{
		SortAt:     row.SortAt.UTC().Format(time.RFC3339Nano),
		ExternalID: row.ExternalID,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeOrderCursor(value string) (OrderCursorRow, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return OrderCursorRow{}, false, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return OrderCursorRow{}, true, errInvalidOrderCursor
	}
	var payload orderCursorPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return OrderCursorRow{}, true, errInvalidOrderCursor
	}
	sortAt, err := time.Parse(time.RFC3339Nano, payload.SortAt)
	if err != nil || strings.TrimSpace(payload.ExternalID) == "" {
		return OrderCursorRow{}, true, errInvalidOrderCursor
	}
	return OrderCursorRow{ExternalID: strings.TrimSpace(payload.ExternalID), SortAt: sortAt}, true, nil
}
