package app

import (
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
)

func (h *HTTPHandler) loginGet(w http.ResponseWriter, r *http.Request) {
	admin, err := h.store.FirstUser(r.Context())
	if err == nil && (!admin.Passhash.Valid || admin.Passhash.String == "") {
		http.Redirect(w, r, "/set-password", http.StatusFound)
		return
	}
	body := `<div class="top"><h1>SHKeeper Go</h1></div><section class="panel">` + flash(r) + `
<form method="post" action="/login">
<label>账号</label><input name="name" autocomplete="username" required>
<label>密码</label><input name="password" type="password" autocomplete="current-password" required>
<p><button type="submit">登录</button></p>
</form></section>`
	page(w, "登录", body)
}

func (h *HTTPHandler) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	user, err := h.store.UserByUsername(r.Context(), r.Form.Get("name"))
	if err != nil || !h.auth.VerifyPassword(user, r.Form.Get("password")) {
		http.Redirect(w, r, "/login?err="+url.QueryEscape("账号或密码不正确"), http.StatusFound)
		return
	}
	if user.TOTPEnabled {
		h.auth.LoginPending2FA(w, user)
		http.Redirect(w, r, "/2fa/verify", http.StatusFound)
		return
	}
	h.auth.Login(w, user)
	http.Redirect(w, r, "/wallets", http.StatusFound)
}

func (h *HTTPHandler) setPasswordGet(w http.ResponseWriter, r *http.Request) {
	admin, err := h.store.FirstUser(r.Context())
	if err == nil && admin.Passhash.Valid && admin.Passhash.String != "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	body := `<div class="top"><h1>初始化管理员</h1></div><section class="panel">` + flash(r) + `
<form method="post" action="/set-password">
<label>新密码</label><input name="pw1" type="password" autocomplete="new-password" required>
<label>确认密码</label><input name="pw2" type="password" autocomplete="new-password" required>
<p><button type="submit">保存</button></p>
</form></section>`
	page(w, "初始化管理员", body)
}

func (h *HTTPHandler) setPasswordPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/set-password?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if r.Form.Get("pw1") != r.Form.Get("pw2") {
		http.Redirect(w, r, "/set-password?err="+url.QueryEscape("两次密码不一致"), http.StatusFound)
		return
	}
	hash, err := h.auth.HashPassword(r.Form.Get("pw1"))
	if err != nil {
		http.Redirect(w, r, "/set-password?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if err := h.store.SetInitialPassword(r.Context(), hash); err != nil {
		http.Redirect(w, r, "/set-password?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login?msg="+url.QueryEscape("管理员密码已设置"), http.StatusFound)
}

func (h *HTTPHandler) logout(w http.ResponseWriter, r *http.Request) {
	h.auth.Logout(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *HTTPHandler) wallets(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString(`<div class="top"><h1>钱包</h1><nav class="nav"><a href="/settings">设置</a><a href="/logout">退出</a></nav></div><section class="panel"><table><thead><tr><th>币种</th><th>名称</th><th>API</th><th>状态</th></tr></thead><tbody>`)
	for _, module := range h.crypto.Modules() {
		wallet, err := h.store.WalletByCrypto(r.Context(), module.Name)
		apiState := "未启用"
		if err == nil && wallet.Enabled {
			apiState = "启用"
		}
		status := h.crypto.Status(r.Context(), module)
		b.WriteString(`<tr><td>`)
		b.WriteString(html.EscapeString(module.Name))
		b.WriteString(`</td><td>`)
		b.WriteString(html.EscapeString(module.DisplayName))
		b.WriteString(`</td><td>`)
		b.WriteString(html.EscapeString(apiState))
		b.WriteString(`</td><td>`)
		b.WriteString(`<span class="` + walletStatusClass(status) + `">`)
		b.WriteString(html.EscapeString(walletStatusLabel(status)))
		b.WriteString(`</span></td></tr>`)
	}
	b.WriteString(`</tbody></table></section>`)
	page(w, "钱包", b.String())
}

func walletStatusLabel(status string) string {
	status = strings.TrimSpace(status)
	switch {
	case strings.EqualFold(status, "Synced"):
		return "已同步"
	case strings.EqualFold(status, "Offline"):
		return "离线"
	case strings.HasPrefix(status, "Sync In Progress (") && strings.HasSuffix(status, ")"):
		detail := strings.TrimSuffix(strings.TrimPrefix(status, "Sync In Progress ("), ")")
		detail = strings.ReplaceAll(detail, " blocks behind", " 个区块落后")
		return "同步中 (" + detail + ")"
	case strings.HasPrefix(status, "Sync In Progress"):
		return strings.Replace(status, "Sync In Progress", "同步中", 1)
	default:
		return status
	}
}

func walletStatusClass(status string) string {
	status = strings.TrimSpace(status)
	switch {
	case strings.EqualFold(status, "Synced"):
		return "ok"
	case strings.EqualFold(status, "Offline"):
		return "err"
	case strings.HasPrefix(status, "Sync In Progress"):
		return "warn"
	default:
		return "muted"
	}
}

func (h *HTTPHandler) settingsGet(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	twoFactorBlock := `<hr><p><a href="/2fa/setup">启用二次验证</a></p>`
	if user.TOTPEnabled {
		twoFactorBlock = `<hr><p class="ok">二次验证已启用</p><p><a href="/2fa/disable">停用二次验证</a> · <a href="/2fa/regenerate-backup">重新生成备用码</a></p>`
	}
	body := `<div class="top"><h1>设置</h1><nav class="nav"><a href="/wallets">钱包</a><a href="/logout">退出</a></nav></div><section class="panel">` + flash(r) + `
<form method="post" action="/settings/account">
<label>管理员账号</label><input name="username" value="` + html.EscapeString(user.Username) + `" autocomplete="username" required>
<label>当前密码</label><input name="current_password" type="password" autocomplete="current-password" required>
<label>新密码</label><input name="new_password" type="password" autocomplete="new-password">
<label>确认新密码</label><input name="confirm_password" type="password" autocomplete="new-password">
<p><button type="submit">保存账号</button></p>
</form>` + twoFactorBlock + `</section>`
	page(w, "设置", body)
}

func (h *HTTPHandler) settingsAccountPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	user, _ := h.auth.CurrentUser(r)
	if !h.auth.VerifyPassword(user, r.Form.Get("current_password")) {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("当前密码不正确"), http.StatusFound)
		return
	}
	hash, err := h.optionalNewPasswordHash(r.Form.Get("new_password"), r.Form.Get("confirm_password"))
	if err != nil {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if err := h.store.UpdateAdminAccount(r.Context(), user.ID, r.Form.Get("username"), hash); err != nil {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape("账号已更新"), http.StatusFound)
}

func (h *HTTPHandler) apiAdminAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := h.auth.CurrentUser(r)
	if !ok {
		if ctxUser, ok := r.Context().Value(userContextKey).(User); ok {
			user = ctxUser
		}
	}
	var req struct {
		Username        string `json:"username"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := readJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	if user.ID == 0 {
		errorJSON(w, http.StatusUnauthorized, errors.New("admin auth required"))
		return
	}
	if !h.auth.VerifyPassword(user, req.CurrentPassword) {
		errorJSON(w, http.StatusForbidden, errors.New("current password is invalid"))
		return
	}
	hash, err := h.optionalNewPasswordHash(req.NewPassword, req.ConfirmPassword)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err)
		return
	}
	if req.Username == "" {
		req.Username = user.Username
	}
	if err := h.store.UpdateAdminAccount(r.Context(), user.ID, req.Username, hash); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		errorJSON(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (h *HTTPHandler) optionalNewPasswordHash(newPassword, confirm string) ([]byte, error) {
	if newPassword == "" && confirm == "" {
		return nil, nil
	}
	if newPassword != confirm {
		return nil, errors.New("new passwords do not match")
	}
	return h.auth.HashPassword(newPassword)
}
