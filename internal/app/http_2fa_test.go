package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTwoFactorSetupAndLoginFlow(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := t.Context()
	setAdminPassword(t, store, cfg, "admin-password")
	handler := newTestHTTPHandler(t, store, cfg)
	user, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/2fa/setup", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res := httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("setup get status=%d body=%s", res.Code, res.Body.String())
	}
	setupCookie := responseCookie(res, setup2FACookieName)
	if setupCookie == nil {
		t.Fatalf("setup cookie missing")
	}
	parts := strings.Split(setupCookie.Value, ":")
	if len(parts) != 3 {
		t.Fatalf("unexpected setup cookie: %s", setupCookie.Value)
	}
	token := currentTOTPToken(t, parts[0])

	form := url.Values{"token": {token}}
	req = httptest.NewRequest(http.MethodPost, "/2fa/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(setupCookie)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "备用码") {
		t.Fatalf("setup post status=%d body=%s", res.Code, res.Body.String())
	}
	enabled, err := store.UserByUsername(ctx, "admin")
	if err != nil || !enabled.TOTPEnabled || !enabled.TOTPSecret.Valid || !enabled.BackupCodes.Valid {
		t.Fatalf("2fa not enabled: user=%+v err=%v", enabled, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/BTC/server", nil)
	req.SetBasicAuth("admin", "admin-password")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "2FA session required") {
		t.Fatalf("basic auth should not bypass 2fa: status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/BTC/server", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, enabled))
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("2fa session admin should access admin api: status=%d body=%s", res.Code, res.Body.String())
	}

	loginForm := url.Values{"name": {"admin"}, "password": {"admin-password"}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusFound || res.Header().Get("Location") != "/2fa/verify" {
		t.Fatalf("login should require 2fa: status=%d location=%s body=%s", res.Code, res.Header().Get("Location"), res.Body.String())
	}
	pendingCookie := responseCookie(res, pending2FACookieName)
	if pendingCookie == nil || responseCookie(res, "shkeeper_session") != nil {
		t.Fatalf("expected pending 2fa cookie without session cookie")
	}

	verifyForm := url.Values{"token": {currentTOTPToken(t, enabled.TOTPSecret.String)}}
	req = httptest.NewRequest(http.MethodPost, "/2fa/verify", strings.NewReader(verifyForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(pendingCookie)
	res = httptest.NewRecorder()
	handler.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusFound || responseCookie(res, "shkeeper_session") == nil {
		t.Fatalf("2fa verify did not create session: status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestTwoFactorBackupCodeIsSingleUse(t *testing.T) {
	store, cfg := testStore(t)
	defer store.Close()
	ctx := t.Context()
	setAdminPassword(t, store, cfg, "admin-password")
	auth := NewAuthManager(cfg, store, testLogger())
	hash, err := auth.HashBackupCode("ABCDE-12345")
	if err != nil {
		t.Fatalf("hash backup code: %v", err)
	}
	data, _ := json.Marshal([]string{hash})
	user, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if err := store.EnableUserTOTP(ctx, user.ID, "JBSWY3DPEHPK3PXP", string(data)); err != nil {
		t.Fatalf("enable 2fa: %v", err)
	}
	enabled, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if !auth.ConsumeBackupCode(ctx, enabled, "ABCDE-12345") {
		t.Fatalf("backup code should verify first time")
	}
	reloaded, err := store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("reload after consume: %v", err)
	}
	if auth.ConsumeBackupCode(ctx, reloaded, "ABCDE-12345") {
		t.Fatalf("backup code should not verify twice")
	}
}

func currentTOTPToken(t *testing.T, secret string) string {
	t.Helper()
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	return totpAtCounter(key, uint64(time.Now().Unix()/30))
}

func responseCookie(res *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
