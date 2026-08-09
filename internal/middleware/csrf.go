package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"

	"cups-web/internal/auth"
)

// RequireSession ensures a valid session cookie exists.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := auth.GetSession(r)
		if err != nil {
			// 免登录模式：注入一个匿名 admin 会话，放行所有受保护接口。
			if auth.AuthDisabled() {
				anon := auth.AnonymousAdminSession()
				ctx := context.WithValue(r.Context(), auth.SessionContextKey, anon)
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin ensures the session belongs to an admin user.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := auth.GetSession(r)
		if err != nil {
			// 免登录模式：视作 admin 放行（管理与驱动页面本就只应在可信内网开放）。
			if auth.AuthDisabled() {
				anon := auth.AnonymousAdminSession()
				ctx := context.WithValue(r.Context(), auth.SessionContextKey, anon)
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if sess.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ValidateCSRF checks that X-CSRF-Token matches csrf_token cookie for state-changing requests.
func ValidateCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 免登录模式：无会话、无 CSRF token，直接放行。
		if auth.AuthDisabled() {
			next.ServeHTTP(w, r)
			return
		}
		// Only validate for state-changing methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("csrf_token")
		if err != nil {
			http.Error(w, "missing csrf cookie", http.StatusForbidden)
			return
		}
		header := r.Header.Get("X-CSRF-Token")
		// 常数时间比较，避免通过响应时序侧信道逐字节推断 token。
		if header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
