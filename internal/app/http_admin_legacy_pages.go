package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
)

func (h *HTTPHandler) walletManagePage(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		http.Redirect(w, r, "/wallets?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	wallet, err := h.store.WalletByCrypto(r.Context(), module.Name)
	if err != nil {
		http.Redirect(w, r, "/wallets?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	destinations, _ := h.store.ListPayoutDestinations(r.Context(), module.Name)
	balance, source, balanceErr := h.crypto.Balance(r.Context(), module)
	status := h.crypto.Status(r.Context(), module)

	var b strings.Builder
	b.WriteString(`<div class="top"><h1>Manage ` + html.EscapeString(module.Name) + `</h1><nav class="nav"><a href="/wallets">Wallets</a><a href="/payout/` + html.EscapeString(module.Name) + `">Payout</a><a href="/settings">Settings</a></nav></div>`)
	b.WriteString(`<section class="panel">` + flash(r))
	b.WriteString(`<table><tbody>`)
	b.WriteString(rowHTML("Display name", module.DisplayName))
	b.WriteString(rowHTML("Network", module.Network))
	b.WriteString(rowHTML("Status", status))
	b.WriteString(rowHTML("Balance", balance.String()+" "+module.Name))
	b.WriteString(rowHTML("Balance source", source))
	if balanceErr != "" {
		b.WriteString(rowHTML("Balance error", balanceErr))
	}
	b.WriteString(rowHTML("API enabled", boolTextEnglish(wallet.Enabled)))
	b.WriteString(rowHTML("API token", nullStringValue(wallet.APIKey)))
	b.WriteString(rowHTML("Autopayout enabled", boolTextEnglish(wallet.Payout)))
	b.WriteString(rowHTML("Autopayout destination", nullStringValue(wallet.PDest)))
	b.WriteString(rowHTML("Autopayout policy", wallet.PPolicy))
	b.WriteString(rowHTML("Confirmations", decimal.NewFromInt(int64(wallet.Confirmations)).String()))
	b.WriteString(`</tbody></table>`)
	if module.Network == "TRX" {
		b.WriteString(`<p><a href="/configure/tron">Configure Tron Settings</a></p>`)
	}
	b.WriteString(`</section>`)
	b.WriteString(`<section class="panel" style="margin-top:16px"><h2>Payout destinations</h2><table><thead><tr><th>Address</th><th>Comment</th></tr></thead><tbody>`)
	for _, item := range destinations {
		b.WriteString(`<tr><td>` + html.EscapeString(item.Addr) + `</td><td>` + html.EscapeString(item.Comment) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table></section>`)
	page(w, "Manage "+module.Name, b.String())
}

func (h *HTTPHandler) payoutPage(w http.ResponseWriter, r *http.Request) {
	module, err := h.moduleFromRoute(r)
	if err != nil {
		http.Redirect(w, r, "/wallets?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	destinations, _ := h.store.ListPayoutDestinations(r.Context(), module.Name)
	var options strings.Builder
	for _, item := range destinations {
		label := item.Addr
		if item.Comment != "" {
			label += " - " + item.Comment
		}
		options.WriteString(`<option value="` + html.EscapeString(item.Addr) + `">` + html.EscapeString(label) + `</option>`)
	}
	body := `<div class="top"><h1>Payout ` + html.EscapeString(module.Name) + `</h1><nav class="nav"><a href="/wallet/` + html.EscapeString(module.Name) + `">Manage</a><a href="/wallets">Wallets</a></nav></div><section class="panel">` + flash(r) + `
<form id="payout-form">
<label>Destination</label><input name="destination" list="payout-destinations" required>
<datalist id="payout-destinations">` + options.String() + `</datalist>
<label>Amount</label><input name="amount" inputmode="decimal" required>
<label>Fee</label><input name="fee" inputmode="decimal">
<label>External ID</label><input name="external_id">
<label>Callback URL</label><input name="callback_url">
<p><button type="submit">Create payout</button></p>
</form><pre id="payout-result" class="muted"></pre>
<script>
document.getElementById('payout-form').addEventListener('submit', async function (event) {
  event.preventDefault();
  const form = new FormData(event.target);
  const payload = Object.fromEntries(form.entries());
  const response = await fetch('/api/v1/` + html.EscapeString(module.Name) + `/payout', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload)
  });
  document.getElementById('payout-result').textContent = JSON.stringify(await response.json(), null, 2);
});
</script></section>`
	page(w, "Payout "+module.Name, body)
}

func (h *HTTPHandler) settingsLocalePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	locale := normalizeLocale(r.Form.Get("locale"))
	http.SetCookie(w, &http.Cookie{
		Name:     "shkeeper_locale",
		Value:    locale,
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape("Locale updated"), http.StatusFound)
}

func normalizeLocale(locale string) string {
	value := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "-", "_"))
	switch value {
	case "zh", "zh_cn", "zh_hans", "cn":
		return "zh_CN"
	default:
		return "en"
	}
}

func (h *HTTPHandler) unlockGet(w http.ResponseWriter, r *http.Request) {
	persistent := h.walletEncryptionPersistentStatus(r)
	runtime := h.walletEncryptionRuntimeStatus(r)
	if persistent == "disabled" {
		http.Redirect(w, r, "/wallets", http.StatusFound)
		return
	}
	var body string
	switch persistent {
	case "pending":
		body = `<div class="top"><h1>Wallet encryption</h1><nav class="nav"><a href="/wallets">Wallets</a></nav></div><section class="panel">` + flash(r) + `
<form method="post" action="/unlock">
<label><input type="checkbox" name="encryption" value="1"> Enable wallet encryption</label>
<label>Encryption password</label><input name="key" type="password" autocomplete="new-password">
<label>Confirm password</label><input name="key2" type="password" autocomplete="new-password">
<label><input type="checkbox" name="confirmation" value="1"> I saved this password</label>
<p><button type="submit">Save</button></p>
</form></section>`
	case "enabled":
		if runtime == "success" {
			body = `<div class="top"><h1>Wallet unlocked</h1><nav class="nav"><a href="/wallets">Wallets</a></nav></div><section class="panel">` + flash(r) + `<p class="ok">Wallet encryption key is loaded.</p></section>`
		} else {
			body = `<div class="top"><h1>Unlock wallet</h1><nav class="nav"><a href="/wallets">Wallets</a></nav></div><section class="panel">` + flash(r) + `
<form method="post" action="/unlock">
<label>Encryption password</label><input name="key" type="password" autocomplete="current-password" required>
<p><button type="submit">Unlock</button></p>
</form></section>`
		}
	default:
		body = `<div class="top"><h1>Wallet encryption</h1></div><section class="panel"><p class="err">Unknown wallet encryption status.</p></section>`
	}
	page(w, "Wallet encryption", body)
}

func (h *HTTPHandler) unlockPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	switch h.walletEncryptionPersistentStatus(r) {
	case "pending":
		if r.Form.Get("encryption") == "" {
			if err := h.store.UpsertSetting(r.Context(), "WalletEncryptionPersistentStatus", "2"); err != nil {
				http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
				return
			}
			http.Redirect(w, r, "/wallets", http.StatusFound)
			return
		}
		key := r.Form.Get("key")
		if key == "" || key != r.Form.Get("key2") {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape("wallet encryption passwords do not match"), http.StatusFound)
			return
		}
		if r.Form.Get("confirmation") == "" {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape("password save confirmation is required"), http.StatusFound)
			return
		}
		hash, err := h.auth.HashPassword(key)
		if err != nil {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
		if err := h.store.UpsertSetting(r.Context(), "WalletEncryptionPasswordHash", string(hash)); err != nil {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
		if err := h.store.UpsertSetting(r.Context(), "WalletEncryptionPersistentStatus", "3"); err != nil {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
		_ = h.store.UpsertSetting(r.Context(), "WalletEncryptionRuntimeStatus", "3")
		http.Redirect(w, r, "/unlock?msg="+url.QueryEscape("Wallet encryption enabled"), http.StatusFound)
	case "enabled":
		hash, err := h.store.Setting(r.Context(), "WalletEncryptionPasswordHash")
		if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(hash) == "" {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape("wallet encryption hash is missing"), http.StatusFound)
			return
		}
		if err != nil {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.Form.Get("key"))) != nil {
			_ = h.store.UpsertSetting(r.Context(), "WalletEncryptionRuntimeStatus", "2")
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape("invalid wallet encryption password"), http.StatusFound)
			return
		}
		if err := h.store.UpsertSetting(r.Context(), "WalletEncryptionRuntimeStatus", "3"); err != nil {
			http.Redirect(w, r, "/unlock?err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
		http.Redirect(w, r, "/unlock?msg="+url.QueryEscape("Wallet unlocked"), http.StatusFound)
	default:
		http.Redirect(w, r, "/wallets", http.StatusFound)
	}
}

func (h *HTTPHandler) tronMultiserverPart(w http.ResponseWriter, r *http.Request) {
	module, err := h.tronModule()
	if err != nil {
		writeHTMLFragment(w, `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	if r.Method == http.MethodPost {
		serverID := strings.TrimSpace(r.URL.Query().Get("server_id"))
		if serverID != "" {
			var ignored map[string]any
			_ = h.crypto.backendJSON(r.Context(), module, http.MethodPost, "/"+module.Name+"/multiserver/change/"+url.PathEscape(serverID), nil, &ignored)
		}
	}
	var payload map[string]any
	if err := h.crypto.backendJSON(r.Context(), module, http.MethodGet, "/"+module.Name+"/multiserver/status", nil, &payload); err != nil {
		writeHTMLFragment(w, `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	writeHTMLFragment(w, tronServersTableHTML(payload))
}

func (h *HTTPHandler) tronConfigurePage(w http.ResponseWriter, r *http.Request) {
	module, err := h.tronModule()
	if err != nil {
		http.Redirect(w, r, "/wallets?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	var staking map[string]any
	var config map[string]any
	stakingErr := h.crypto.backendJSON(r.Context(), module, http.MethodGet, "/staking", nil, &staking)
	configErr := h.crypto.backendJSON(r.Context(), module, http.MethodGet, "/staking/info", nil, &config)
	var b strings.Builder
	b.WriteString(`<div class="top"><h1>Tron Settings</h1><nav class="nav"><a href="/wallet/TRX">TRX wallet</a><a href="/wallets">Wallets</a></nav></div>`)
	b.WriteString(`<section class="panel"><h2>Servers</h2><div id="tron-servers">`)
	var servers map[string]any
	if err := h.crypto.backendJSON(r.Context(), module, http.MethodGet, "/"+module.Name+"/multiserver/status", nil, &servers); err == nil {
		b.WriteString(tronServersTableHTML(servers))
	} else {
		b.WriteString(`<p class="err">` + html.EscapeString(err.Error()) + `</p>`)
	}
	b.WriteString(`</div></section>`)
	b.WriteString(`<section class="panel" style="margin-top:16px"><h2>Staking</h2>`)
	if configErr != nil {
		b.WriteString(`<p class="err">` + html.EscapeString(configErr.Error()) + `</p>`)
	} else {
		b.WriteString(`<pre>` + html.EscapeString(prettyJSON(config)) + `</pre>`)
	}
	if stakingErr != nil {
		b.WriteString(`<p class="err">` + html.EscapeString(stakingErr.Error()) + `</p>`)
	} else {
		b.WriteString(`<pre>` + html.EscapeString(prettyJSON(staking)) + `</pre>`)
	}
	b.WriteString(tronStakingFormHTML())
	b.WriteString(`</section>`)
	page(w, "Tron Settings", b.String())
}

func (h *HTTPHandler) tronStakingStakeGet(w http.ResponseWriter, r *http.Request) {
	writeHTMLFragment(w, tronStakingFormHTML())
}

func (h *HTTPHandler) tronStakingStakePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeHTMLFragment(w, `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	module, err := h.tronModule()
	if err != nil {
		writeHTMLFragment(w, `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	amount := strings.TrimSpace(firstNonEmptyString(r.Form.Get("amount_trx"), r.Form.Get("amount")))
	resource := strings.ToUpper(strings.TrimSpace(r.Form.Get("resource")))
	var payload map[string]any
	err = h.crypto.backendJSON(r.Context(), module, http.MethodPost, "/staking/freeze/"+url.PathEscape(amount)+"/"+url.PathEscape(resource), nil, &payload)
	if err != nil {
		writeHTMLFragment(w, `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	writeHTMLFragment(w, `<pre>`+html.EscapeString(prettyJSON(payload))+`</pre>`)
}

func (h *HTTPHandler) tronModule() (*CryptoModule, error) {
	for _, name := range []string{"TRX", "USDT", "USDC"} {
		if module, ok := h.crypto.Module(name); ok && module.Network == "TRX" {
			return module, nil
		}
	}
	for _, module := range h.crypto.Modules() {
		if module.Network == "TRX" {
			return module, nil
		}
	}
	return nil, errors.New("TRON module is unavailable")
}

func tronServersTableHTML(payload map[string]any) string {
	rows := mapSlice(firstAny(payload, "statuses"))
	sort.SliceStable(rows, func(i, j int) bool {
		return anyString(rows[i]["id"]) < anyString(rows[j]["id"])
	})
	var b strings.Builder
	b.WriteString(`<table><thead><tr><th>Name</th><th>Status</th><th>Lag</th><th>Version</th><th>Action</th></tr></thead><tbody>`)
	for _, row := range rows {
		id := anyString(row["id"])
		name := firstNonEmptyString(anyString(row["name"]), anyString(row["url"]))
		status := anyString(row["status"])
		active := boolFromAny(row["is_active"], false)
		lag := ""
		version := ""
		if info, ok := row["node_info"].(map[string]any); ok {
			lag = anyString(info["lag"])
			if cfg, ok := info["configNodeInfo"].(map[string]any); ok {
				version = anyString(cfg["codeVersion"])
			}
		}
		action := ""
		if !active {
			action = `<form method="post" action="/parts/tron-multiserver?server_id=` + url.QueryEscape(id) + `"><button class="secondary" type="submit">Make active</button></form>`
		}
		label := "Offline"
		if strings.EqualFold(status, "success") {
			label = "Online"
		}
		if active {
			label += ", Active"
		}
		b.WriteString(`<tr><td title="` + html.EscapeString(anyString(row["url"])) + `">` + html.EscapeString(name) + `</td><td>` + html.EscapeString(label) + `</td><td>` + html.EscapeString(lag) + `</td><td>` + html.EscapeString(version) + `</td><td>` + action + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func tronStakingFormHTML() string {
	return `<form method="post" action="/parts/tron-staking-stake">
<label>Amount TRX</label><input name="amount_trx" inputmode="decimal" required>
<label>Resource</label><select name="resource"><option value="ENERGY">ENERGY</option><option value="BANDWIDTH">BANDWIDTH</option></select>
<p><button type="submit">Stake</button></p>
</form>`
}

func rowHTML(label, value string) string {
	return `<tr><th>` + html.EscapeString(label) + `</th><td>` + html.EscapeString(value) + `</td></tr>`
}

func writeHTMLFragment(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func prettyJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return anyString(value)
	}
	return string(data)
}

func mapSlice(value any) []map[string]any {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func boolTextEnglish(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func backendJSON(ctx context.Context, registry *CryptoRegistry, module *CryptoModule, method, path string, out any) error {
	return registry.backendJSON(ctx, module, method, path, nil, out)
}
