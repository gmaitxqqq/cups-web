package middleware

import (
	"net/http"
	"os"
	"strings"
)

// CrossOriginProtection 返回 Go 1.25 内置的 CSRF 防护中间件。它基于浏览器自
// 2023 年起普遍发送的 Sec-Fetch-Site 头（或 Origin 与 Host 比对）拒绝跨源的非
// 安全请求（POST/PUT/DELETE 等），作为 double-submit cookie（见 csrf.go）之外
// 的纵深防御。安全方法（GET/HEAD/OPTIONS）与非浏览器客户端（无 Sec-Fetch-Site）
// 会被放行，因此不影响正常的同源前端与 API 调用。
//
// 若前后端在不同源部署（例如本地开发把 Vite :5173 直连后端，而非走代理），
// 可用环境变量 TRUSTED_ORIGINS（逗号分隔，形如 http://localhost:5173）声明可信源。
func CrossOriginProtection() func(http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if v := strings.TrimSpace(os.Getenv("TRUSTED_ORIGINS")); v != "" {
		for o := range strings.SplitSeq(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				_ = cop.AddTrustedOrigin(o)
			}
		}
	}
	return cop.Handler
}

// SecurityHeaders 为所有响应加上一组基础安全响应头。
//
// CSP 说明：本项目是同源 SPA。前端 pdf.js 依赖 Web Worker（blob:）与 wasm，
// Vite/Nuxt UI/Tailwind 会注入 inline script/style，故 script-src/style-src 放宽
// 到 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob: 以避免打断功能；但仍
// 锁死 object-src、base-uri 与 frame-ancestors，收敛点击劫持、插件注入与 <base>
// 篡改等向量。若后续前端收敛了 inline 用法，可进一步收紧 script-src。
//
// connect-src 必须包含 blob:：PdfCanvas 把转换/标准化后的 PDF 存成 blob: URL 后交给
// pdf.js 的 getDocument({url}) 渲染预览，pdf.js 内部用 fetch 拉取该 URL，受 connect-src
// 管控——漏掉 blob: 会导致所有 PDF 预览「加载失败」（图片走 img-src 不受影响，见 Issue #86）。
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob:; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"font-src 'self' data:; " +
		"worker-src 'self' blob:; " +
		"connect-src 'self' blob:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}
