package chainworker

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) activationStatus(w http.ResponseWriter, r *http.Request) {
	crypto := strings.ToUpper(chi.URLParam(r, "crypto"))
	if s.cfg.Module != "TRON" {
		writeJSON(w, http.StatusOK, map[string]any{
			"required": false,
			"module":   s.cfg.Module,
			"crypto":   crypto,
		})
		return
	}
	report, err := s.tronActivationStatus(r.Context(), crypto)
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) tronActivationStatus(ctx context.Context, crypto string) (map[string]any, error) {
	accounts, err := s.accountsForCrypto(ctx, crypto)
	if err != nil {
		return nil, err
	}
	maxChecks := intEnv("TRON_ACTIVATION_CHECK_LIMIT", 3)
	if maxChecks <= 0 {
		maxChecks = len(accounts)
	}
	checked := 0
	active := 0
	inactive := 0
	sampleInactive := make([]string, 0, 5)
	errorsSeen := make([]string, 0)
	for _, account := range accounts {
		if checked >= maxChecks {
			break
		}
		activated, err := s.tronAccountActivated(ctx, account.Address)
		checked++
		if err != nil {
			if len(errorsSeen) < 3 {
				errorsSeen = append(errorsSeen, fmt.Sprintf("%s: %s", account.Address, err.Error()))
			}
			continue
		}
		if activated {
			active++
			continue
		}
		inactive++
		if len(sampleInactive) < 5 {
			sampleInactive = append(sampleInactive, account.Address)
		}
	}
	return map[string]any{
		"required":         true,
		"module":           "TRON",
		"crypto":           crypto,
		"total":            len(accounts),
		"checked":          checked,
		"active":           active,
		"inactive":         inactive,
		"truncated":        checked < len(accounts),
		"errors":           errorsSeen,
		"sample_inactive":  sampleInactive,
		"activation_asset": "TRX",
		"activation_hint":  "向未激活地址转入少量 TRX 即可激活账户",
		"fee_hint":         "TRC20 提现仍需要保留 TRX 支付网络资源或手续费",
	}, nil
}

func (s *Server) tronAccountActivated(ctx context.Context, address string) (bool, error) {
	addressHex, err := tronAddressHex(address)
	if err != nil {
		return false, err
	}
	var resp map[string]any
	if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getaccount", map[string]any{
		"address": addressHex,
		"visible": false,
	}, &resp); err != nil {
		return false, err
	}
	if len(resp) == 0 {
		return false, nil
	}
	if value, ok := resp["address"].(string); ok && strings.TrimSpace(value) != "" {
		return true, nil
	}
	for _, key := range []string{"balance", "assetV2", "account_resource", "create_time", "latest_opration_time", "frozenV2"} {
		if _, ok := resp[key]; ok {
			return true, nil
		}
	}
	return false, nil
}
