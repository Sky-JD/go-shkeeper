package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const userContextKey contextKey = "user"

const pending2FACookieName = "shkeeper_pending_2fa"

type AuthManager struct {
	cfg    Config
	store  *Store
	logger *slog.Logger
}

func NewAuthManager(cfg Config, store *Store, logger *slog.Logger) *AuthManager {
	return &AuthManager{cfg: cfg, store: store, logger: logger}
}

func (a *AuthManager) HashPassword(password string) ([]byte, error) {
	if len(password) < 6 {
		return nil, errors.New("password must be at least 6 characters")
	}
	return bcrypt.GenerateFromPassword([]byte(password), 12)
}

func (a *AuthManager) VerifyPassword(user User, password string) bool {
	if !user.Passhash.Valid || user.Passhash.String == "" {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(user.Passhash.String), []byte(password))
	return err == nil
}

func (a *AuthManager) Login(w http.ResponseWriter, user User) {
	exp := time.Now().Add(24 * time.Hour).Unix()
	payload := fmt.Sprintf("%d:%d", user.ID, exp)
	sig := a.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     "shkeeper_session",
		Value:    payload + ":" + sig,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(exp, 0),
	})
	a.ClearPending2FA(w)
}

func (a *AuthManager) LoginPending2FA(w http.ResponseWriter, user User) {
	exp := time.Now().Add(10 * time.Minute).Unix()
	payload := fmt.Sprintf("%d:%d", user.ID, exp)
	sig := a.sign("2fa:" + payload)
	http.SetCookie(w, &http.Cookie{
		Name:     pending2FACookieName,
		Value:    payload + ":" + sig,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(exp, 0),
	})
}

func (a *AuthManager) Logout(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "shkeeper_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
	a.ClearPending2FA(w)
}

func (a *AuthManager) ClearPending2FA(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     pending2FACookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func (a *AuthManager) Pending2FAUser(r *http.Request) (User, bool) {
	cookie, err := r.Cookie(pending2FACookieName)
	if err != nil {
		return User{}, false
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 {
		return User{}, false
	}
	payload := parts[0] + ":" + parts[1]
	if subtle.ConstantTimeCompare([]byte(a.sign("2fa:"+payload)), []byte(parts[2])) != 1 {
		return User{}, false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return User{}, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return User{}, false
	}
	user, err := a.store.UserByID(r.Context(), id)
	if err != nil || !user.TOTPEnabled {
		return User{}, false
	}
	return user, true
}

func (a *AuthManager) CurrentUser(r *http.Request) (User, bool) {
	if user, ok := r.Context().Value(userContextKey).(User); ok {
		return user, true
	}
	cookie, err := r.Cookie("shkeeper_session")
	if err != nil {
		return User{}, false
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 {
		return User{}, false
	}
	payload := parts[0] + ":" + parts[1]
	if subtle.ConstantTimeCompare([]byte(a.sign(payload)), []byte(parts[2])) != 1 {
		return User{}, false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return User{}, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return User{}, false
	}
	user, err := a.store.UserByID(r.Context(), id)
	if err != nil || !user.Passhash.Valid || user.Passhash.String == "" {
		return User{}, false
	}
	return user, true
}

func (a *AuthManager) WithUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, ok := a.CurrentUser(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), userContextKey, user))
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AuthManager) RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.CurrentUser(r); !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "login required"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AuthManager) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Shkeeper-Api-Key")
		if key == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "No API key"})
			return
		}
		if _, err := a.store.WalletByAPIKey(r.Context(), key); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "Bad API key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AuthManager) RequireAdminOrBasic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.CurrentUser(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "authorization required"})
			return
		}
		user, err := a.store.UserByUsername(r.Context(), username)
		if err != nil || !a.VerifyPassword(user, password) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "Bad HTTP Basic Auth credentials"})
			return
		}
		if user.TOTPEnabled {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "error", "message": "2FA session required"})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, user))
		next.ServeHTTP(w, r)
	})
}

func (a *AuthManager) GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(buf), "="), nil
}

func (a *AuthManager) VerifyTOTP(user User, token string) bool {
	if !user.TOTPEnabled || !user.TOTPSecret.Valid {
		return false
	}
	return verifyTOTPCode(user.TOTPSecret.String, token, time.Now())
}

func (a *AuthManager) GenerateBackupCodes(count int) ([]string, string, error) {
	if count <= 0 {
		count = 10
	}
	codes := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for len(codes) < count {
		code, err := randomBackupCode()
		if err != nil {
			return nil, "", err
		}
		hash, err := a.HashBackupCode(code)
		if err != nil {
			return nil, "", err
		}
		codes = append(codes, code)
		hashes = append(hashes, hash)
	}
	data, err := json.Marshal(hashes)
	return codes, string(data), err
}

func (a *AuthManager) HashBackupCode(code string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(normalizeBackupCode(code)), 12)
	return string(hash), err
}

func (a *AuthManager) ConsumeBackupCode(ctx context.Context, user User, code string) bool {
	if !user.BackupCodes.Valid || strings.TrimSpace(user.BackupCodes.String) == "" {
		return false
	}
	var hashes []string
	if err := json.Unmarshal([]byte(user.BackupCodes.String), &hashes); err != nil {
		return false
	}
	normalized := []byte(normalizeBackupCode(code))
	for i, hash := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(hash), normalized) == nil {
			hashes = append(hashes[:i], hashes[i+1:]...)
			data, err := json.Marshal(hashes)
			if err != nil {
				return false
			}
			return a.store.UpdateUserBackupCodes(ctx, user.ID, string(data)) == nil
		}
	}
	return false
}

func (a *AuthManager) sign(payload string) string {
	mac := hmac.New(sha256.New, a.cfg.SecretKey)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyTOTPCode(secret, token string, now time.Time) bool {
	token = strings.TrimSpace(token)
	if len(token) != 6 {
		return false
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false
	}
	counter := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		if subtle.ConstantTimeCompare([]byte(totpAtCounter(key, uint64(counter+offset))), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if missing := len(secret) % 8; missing != 0 {
		secret += strings.Repeat("=", 8-missing)
	}
	return base32.StdEncoding.DecodeString(secret)
}

func totpAtCounter(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 | (uint32(sum[offset+1])&0xff)<<16 | (uint32(sum[offset+2])&0xff)<<8 | (uint32(sum[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", value%1000000)
}

func randomBackupCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	text := strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
	if len(text) > 10 {
		text = text[:10]
	}
	return text[:5] + "-" + text[5:], nil
}

func normalizeBackupCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
