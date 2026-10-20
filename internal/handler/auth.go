package handler

import (
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// AuthConfig 返回认证方式与登录地址，供前端决定展示本地表单或跳转 SSO。
func AuthConfig(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		loginURL := s.Auth.LoginURL("")
		// Casdoor：启动 OAuth 流程，写入 nonce cookie
		if cd, ok := s.Auth.(*auth.Casdoor); ok {
			loginURL = cd.BeginLogin(w)
		}
		httpx.OK(w, map[string]any{
			"provider":  s.Auth.Name(),
			"login_url": loginURL,
		})
	}
}

// Login 内置账号登录。
func Login(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		u, token, err := s.Auth.Login(r.Context(), req.Username, req.Password)
		if err != nil {
			// 登录失败必须留痕：识别暴力破解的主要依据。
			auditAnon(s, r, audit.ActionLogin, req.Username, "auth", "", err)
			httpx.Fail(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		auditAnon(s, r, audit.ActionLogin, req.Username, "auth", "", nil)
		s.Auth.SetSession(w, token)
		httpx.OK(w, u)
	}
}

// Logout 注销。
func Logout(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.Auth.ClearSession(w)
		httpx.OK(w, nil)
	}
}

// UserInfo 返回当前用户。
func UserInfo(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.FromContext(r.Context())
		httpx.OK(w, u)
	}
}

// AuthCallback OAuth2 授权码回调（casdoor）。
//
// 流程：Casdoor 重定向携带 code+state → 校验 nonce cookie（防 CSRF）→ 用 code 换 token
// → 拉取用户信息 → 签发本系统会话 cookie → 重定向回前端首页。
// 仅当 Provider 为 casdoor 时可用；builtin 下返回 404，避免暴露无用端点。
func AuthCallback(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cd, ok := s.Auth.(*auth.Casdoor)
		if !ok {
			httpx.Fail(w, http.StatusNotFound, "当前认证方式不支持 OAuth 回调")
			return
		}
		u, token, err := cd.HandleCallbackRequest(r.Context(), w, r)
		if err != nil {
			auditAnon(s, r, audit.ActionLogin, "", "auth", "", err)
			httpx.Fail(w, http.StatusUnauthorized, "SSO 登录失败")
			return
		}
		auditAnon(s, r, audit.ActionLogin, u.Username, "auth", "", nil)
		cd.SetSession(w, token)
		// 会话已写入 cookie，重定向回前端首页。
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
