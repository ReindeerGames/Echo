package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type auth struct {
	adminToken    []byte
	sessionSecret []byte
	secure        bool
	devMode       bool
}
type sessionClaims struct {
	Actor   string `json:"actor"`
	CSRF    string `json:"csrf"`
	Expires int64  `json:"expires"`
}

func newAuth(adminToken, sessionSecret string, secure, devMode bool) *auth {
	return &auth{adminToken: []byte(adminToken), sessionSecret: []byte(sessionSecret), secure: secure, devMode: devMode}
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !a.devMode && !constantEqual([]byte(input.Token), a.adminToken) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid admin token")
		return
	}
	csrf := randomHex(24)
	claims := sessionClaims{Actor: "owner", CSRF: csrf, Expires: time.Now().Add(12 * time.Hour).Unix()}
	signed, err := a.sign(claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "echo_session", Value: signed, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds())})
	http.SetCookie(w, &http.Cookie{Name: "echo_csrf", Value: csrf, Path: "/", HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"actor": claims.Actor, "csrf": csrf, "expiresAt": time.Unix(claims.Expires, 0).UTC()})
}

func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "echo_session", Value: "", Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "echo_csrf", Value: "", Path: "/", Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (a *auth) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.devMode {
			next.ServeHTTP(w, r.WithContext(withActor(r.Context(), "dev-owner")))
			return
		}
		cookie, err := r.Cookie("echo_session")
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "sign in to continue")
			return
		}
		claims, err := a.verify(cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session_expired", "sign in again")
			return
		}
		if mutating(r.Method) {
			csrfCookie, err := r.Cookie("echo_csrf")
			if err != nil || !constantEqual([]byte(r.Header.Get("X-Echo-CSRF")), []byte(csrfCookie.Value)) || !constantEqual([]byte(claims.CSRF), []byte(csrfCookie.Value)) {
				writeError(w, http.StatusForbidden, "csrf_failed", "request verification failed")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(withActor(r.Context(), claims.Actor)))
	})
}

func (a *auth) sign(claims sessionClaims) (string, error) {
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, a.sessionSecret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (a *auth) verify(value string) (sessionClaims, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return sessionClaims{}, errors.New("invalid session")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return sessionClaims{}, err
	}
	mac := hmac.New(sha256.New, a.sessionSecret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return sessionClaims{}, errors.New("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return sessionClaims{}, err
	}
	var claims sessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return sessionClaims{}, err
	}
	if claims.Expires < time.Now().Unix() {
		return sessionClaims{}, errors.New("expired")
	}
	return claims, nil
}
func constantEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}
func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}
func mutating(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
