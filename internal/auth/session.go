package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"cups-web/internal/store"

	"github.com/gorilla/securecookie"
)

var (
	secureCookieOnce sync.Once
	secureCookieFlag bool
)

// CookieSecure 报告是否应在会话 / CSRF cookie 上设置 Secure 属性。由环境变量
// COOKIE_SECURE=true 开启（HTTPS 部署应开启），默认关闭以兼容 HTTP 内网部署。
func CookieSecure() bool {
	secureCookieOnce.Do(func() {
		secureCookieFlag = strings.EqualFold(strings.TrimSpace(os.Getenv("COOKIE_SECURE")), "true")
	})
	return secureCookieFlag
}

var s *securecookie.SecureCookie

const sessionCookieName = "session"
const csrfCookieName = "csrf_token"

// SessionContextKey 用于在请求上下文中传递「认证已禁用时的合成会话」。
// RequireSession/RequireAdmin 中间件在 AUTH_DISABLED 模式下会把一个匿名
// admin 会话塞进 context，下游 handler 通过 auth.GetSession(r) 即可拿到，
// 无需改造各个 handler 的取会话逻辑。
type contextKey string

const SessionContextKey contextKey = "cups-web.anon-session"

var (
	authDisabledOnce sync.Once
	authDisabledFlag bool
)

// AuthDisabled 报告是否以「免登录」模式运行（环境变量 AUTH_DISABLED=true/1/on）。
// 内网自用、不暴露在公网时可用，启动即无需用户名密码直接打开主界面。
func AuthDisabled() bool {
	authDisabledOnce.Do(func() {
		v := strings.TrimSpace(os.Getenv("AUTH_DISABLED"))
		switch strings.ToLower(v) {
		case "true", "1", "on", "yes":
			authDisabledFlag = true
		default:
			authDisabledFlag = false
		}
	})
	return authDisabledFlag
}

// AnonymousAdminSession 返回认证禁用模式下使用的合成 admin 会话。
// 仅用于绕过鉴权与记录打印历史（UserID=0 表示匿名），不对应任何真实用户。
func AnonymousAdminSession() Session {
	return Session{
		UserID:   0,
		Username: "anonymous",
		Role:     "admin",
		Expires:  time.Now().Add(365 * 24 * time.Hour),
	}
}

const (
	settingHashKey  = "session_hash_key"
	settingBlockKey = "session_block_key"
)

func SetupSecureCookie(db *sql.DB) error {
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hashKeyStr, err := store.GetSettingString(ctx, tx, settingHashKey, "")
	if err != nil {
		return err
	}
	blockKeyStr, err := store.GetSettingString(ctx, tx, settingBlockKey, "")
	if err != nil {
		return err
	}

	var hashKey, blockKey []byte

	if hashKeyStr == "" {
		hashKey = securecookie.GenerateRandomKey(32)
		hashKeyStr = base64.StdEncoding.EncodeToString(hashKey)
		if err := store.SetSettingString(ctx, tx, settingHashKey, hashKeyStr); err != nil {
			return err
		}
	} else {
		hashKey, _ = base64.StdEncoding.DecodeString(hashKeyStr)
		if len(hashKey) == 0 {
			hashKey = []byte(hashKeyStr)
		}
	}

	if blockKeyStr == "" {
		blockKey = securecookie.GenerateRandomKey(32)
		blockKeyStr = base64.StdEncoding.EncodeToString(blockKey)
		if err := store.SetSettingString(ctx, tx, settingBlockKey, blockKeyStr); err != nil {
			return err
		}
	} else {
		blockKey, _ = base64.StdEncoding.DecodeString(blockKeyStr)
		if len(blockKey) == 0 {
			blockKey = []byte(blockKeyStr)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s = securecookie.New(hashKey, blockKey)
	return nil
}

type Session struct {
	UserID   int64     `json:"userId"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Expires  time.Time `json:"expires"`
}

func SetSession(w http.ResponseWriter, sess Session) error {
	if s == nil {
		return errors.New("securecookie not initialized")
	}
	encoded, err := s.Encode(sessionCookieName, sess)
	if err != nil {
		return err
	}
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	}
	http.SetCookie(w, cookie)
	return nil
}

func ClearSession(w http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
	http.SetCookie(w, cookie)
	// clear csrf cookie too
	csrf := &http.Cookie{
		Name:   csrfCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	}
	http.SetCookie(w, csrf)
}

func GetSession(r *http.Request) (Session, error) {
	var sess Session
	if s == nil {
		return sess, errors.New("securecookie not initialized")
	}
	// 认证禁用模式下，中间件注入的合成会话优先返回。
	if v := r.Context().Value(SessionContextKey); v != nil {
		if anon, ok := v.(Session); ok {
			return anon, nil
		}
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sess, err
	}
	err = s.Decode(sessionCookieName, c.Value, &sess)
	if err != nil {
		return sess, err
	}
	return sess, nil
}
