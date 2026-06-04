package app

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const setup2FACookieName = "shkeeper_setup_2fa"

func (h *HTTPHandler) twoFactorVerifyGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.auth.Pending2FAUser(r); !ok {
		http.Redirect(w, r, "/login?err="+url.QueryEscape("请先登录"), http.StatusFound)
		return
	}
	body := `<div class="top"><h1>二次验证</h1></div><section class="panel">` + flash(r) + `
<form method="post" action="/2fa/verify">
<label>动态验证码或备用码</label><input name="token" inputmode="numeric" autocomplete="one-time-code" required>
<label><input name="use_backup" type="checkbox" value="1" style="width:auto"> 使用备用码</label>
<p><button type="submit">验证</button></p>
</form></section>`
	page(w, "二次验证", body)
}

func (h *HTTPHandler) twoFactorVerifyPost(w http.ResponseWriter, r *http.Request) {
	user, ok := h.auth.Pending2FAUser(r)
	if !ok {
		http.Redirect(w, r, "/login?err="+url.QueryEscape("请先登录"), http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/2fa/verify?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	token := r.Form.Get("token")
	useBackup := r.Form.Get("use_backup") == "1"
	verified := false
	if useBackup {
		verified = h.auth.ConsumeBackupCode(r.Context(), user, token)
	} else {
		verified = h.auth.VerifyTOTP(user, compactOTPToken(token))
	}
	if !verified {
		http.Redirect(w, r, "/2fa/verify?err="+url.QueryEscape("验证码无效"), http.StatusFound)
		return
	}
	h.auth.Login(w, user)
	http.Redirect(w, r, "/wallets", http.StatusFound)
}

func (h *HTTPHandler) twoFactorSetupGet(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	if user.TOTPEnabled {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("二次验证已启用"), http.StatusFound)
		return
	}
	secret := h.setup2FASecret(w, r)
	body := `<div class="top"><h1>启用二次验证</h1><nav class="nav"><a href="/settings">设置</a></nav></div><section class="panel">` + flash(r) + `
<p class="muted">在认证器中添加下面的密钥，或使用 otpauth URI。</p>
<p><strong>` + html.EscapeString(secret) + `</strong></p>
<p class="muted">` + html.EscapeString(otpAuthURI(user.Username, secret)) + `</p>
<form method="post" action="/2fa/setup">
<label>动态验证码</label><input name="token" inputmode="numeric" autocomplete="one-time-code" required>
<p><button type="submit">启用</button></p>
</form></section>`
	page(w, "启用二次验证", body)
}

func (h *HTTPHandler) twoFactorSetupPost(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	if user.TOTPEnabled {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("二次验证已启用"), http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/2fa/setup?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	secret, ok := h.readSetup2FASecret(r)
	if !ok {
		http.Redirect(w, r, "/2fa/setup?err="+url.QueryEscape("会话已过期，请重试"), http.StatusFound)
		return
	}
	if !verifyTOTPCode(secret, compactOTPToken(r.Form.Get("token")), time.Now()) {
		http.Redirect(w, r, "/2fa/setup?err="+url.QueryEscape("验证码无效"), http.StatusFound)
		return
	}
	codes, codesJSON, err := h.auth.GenerateBackupCodes(10)
	if err != nil {
		http.Redirect(w, r, "/2fa/setup?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if err := h.store.EnableUserTOTP(r.Context(), user.ID, secret, codesJSON); err != nil {
		http.Redirect(w, r, "/2fa/setup?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	clearSetup2FACookie(w)
	page(w, "备用码", backupCodesHTML("二次验证已启用", codes))
}

func (h *HTTPHandler) twoFactorDisableGet(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	if !user.TOTPEnabled {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("二次验证未启用"), http.StatusFound)
		return
	}
	body := `<div class="top"><h1>停用二次验证</h1><nav class="nav"><a href="/settings">设置</a></nav></div><section class="panel">` + flash(r) + `
<form method="post" action="/2fa/disable">
<label>当前密码</label><input name="password" type="password" autocomplete="current-password" required>
<label>动态验证码</label><input name="token" inputmode="numeric" autocomplete="one-time-code" required>
<p><button type="submit">停用</button></p>
</form></section>`
	page(w, "停用二次验证", body)
}

func (h *HTTPHandler) twoFactorDisablePost(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	if !user.TOTPEnabled {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("二次验证未启用"), http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/2fa/disable?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if !h.auth.VerifyPassword(user, r.Form.Get("password")) {
		http.Redirect(w, r, "/2fa/disable?err="+url.QueryEscape("当前密码不正确"), http.StatusFound)
		return
	}
	if !h.auth.VerifyTOTP(user, compactOTPToken(r.Form.Get("token"))) {
		http.Redirect(w, r, "/2fa/disable?err="+url.QueryEscape("验证码无效"), http.StatusFound)
		return
	}
	if err := h.store.DisableUserTOTP(r.Context(), user.ID); err != nil {
		http.Redirect(w, r, "/2fa/disable?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape("二次验证已停用"), http.StatusFound)
}

func (h *HTTPHandler) twoFactorRegenerateBackupGet(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	if !user.TOTPEnabled {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("二次验证未启用"), http.StatusFound)
		return
	}
	body := `<div class="top"><h1>重新生成备用码</h1><nav class="nav"><a href="/settings">设置</a></nav></div><section class="panel">` + flash(r) + `
<form method="post" action="/2fa/regenerate-backup">
<label>当前密码</label><input name="password" type="password" autocomplete="current-password" required>
<label>动态验证码</label><input name="token" inputmode="numeric" autocomplete="one-time-code" required>
<p><button type="submit">重新生成</button></p>
</form></section>`
	page(w, "重新生成备用码", body)
}

func (h *HTTPHandler) twoFactorRegenerateBackupPost(w http.ResponseWriter, r *http.Request) {
	user, _ := h.auth.CurrentUser(r)
	if !user.TOTPEnabled {
		http.Redirect(w, r, "/settings?err="+url.QueryEscape("二次验证未启用"), http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/2fa/regenerate-backup?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if !h.auth.VerifyPassword(user, r.Form.Get("password")) {
		http.Redirect(w, r, "/2fa/regenerate-backup?err="+url.QueryEscape("当前密码不正确"), http.StatusFound)
		return
	}
	if !h.auth.VerifyTOTP(user, compactOTPToken(r.Form.Get("token"))) {
		http.Redirect(w, r, "/2fa/regenerate-backup?err="+url.QueryEscape("验证码无效"), http.StatusFound)
		return
	}
	codes, codesJSON, err := h.auth.GenerateBackupCodes(10)
	if err != nil {
		http.Redirect(w, r, "/2fa/regenerate-backup?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if err := h.store.UpdateUserBackupCodes(r.Context(), user.ID, codesJSON); err != nil {
		http.Redirect(w, r, "/2fa/regenerate-backup?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	page(w, "备用码", backupCodesHTML("备用码已重新生成", codes))
}

func (h *HTTPHandler) setup2FASecret(w http.ResponseWriter, r *http.Request) string {
	if secret, ok := h.readSetup2FASecret(r); ok {
		return secret
	}
	secret, err := h.auth.GenerateTOTPSecret()
	if err != nil {
		secret = "CHANGESECRET"
	}
	exp := time.Now().Add(10 * time.Minute).Unix()
	payload := secret + ":" + stringFromInt64(exp)
	sig := h.auth.sign("setup2fa:" + payload)
	http.SetCookie(w, &http.Cookie{
		Name:     setup2FACookieName,
		Value:    payload + ":" + sig,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(exp, 0),
	})
	return secret
}

func (h *HTTPHandler) readSetup2FASecret(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(setup2FACookieName)
	if err != nil {
		return "", false
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + ":" + parts[1]
	if h.auth.sign("setup2fa:"+payload) != parts[2] {
		return "", false
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > unix {
		return "", false
	}
	return parts[0], true
}

func clearSetup2FACookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: setup2FACookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Unix(0, 0), MaxAge: -1})
}

func backupCodesHTML(title string, codes []string) string {
	var b strings.Builder
	b.WriteString(`<div class="top"><h1>` + html.EscapeString(title) + `</h1><nav class="nav"><a href="/settings">设置</a></nav></div><section class="panel"><p class="muted">请保存这些备用码。每个备用码只能使用一次。</p><table><tbody>`)
	for _, code := range codes {
		b.WriteString(`<tr><td><strong>` + html.EscapeString(code) + `</strong></td></tr>`)
	}
	b.WriteString(`</tbody></table></section>`)
	return b.String()
}

func otpAuthURI(username, secret string) string {
	label := url.QueryEscape("SHKeeper.io:" + username)
	return "otpauth://totp/" + label + "?secret=" + url.QueryEscape(secret) + "&issuer=SHKeeper.io"
}

func compactOTPToken(token string) string {
	token = strings.ReplaceAll(token, " ", "")
	return strings.ReplaceAll(token, "-", "")
}

func stringFromInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
