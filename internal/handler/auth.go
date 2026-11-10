package handler

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
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
		// casdoor 模式不走本地密码：明确提示改用 SSO。
		// 旧版前端/缓存页面仍会提交密码表单，若返回「用户名或密码错误」会误导排查。
		if _, ok := s.Auth.(*auth.Casdoor); ok {
			auditAnon(s, r, audit.ActionLogin, "", "auth", "", errors.New("casdoor 模式请使用 SSO 登录"))
			httpx.Fail(w, http.StatusBadRequest, "当前为 FlyIAM 统一登录模式，请刷新页面后点击「使用 FlyIAM 登录」")
			return
		}
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
// 失败时重定向到 /?sso_error=...，由登录页展示具体原因（同时写审计与日志）。
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
			// 记录具体原因：回调失败时浏览器只能看到提示，没有日志很难判断是
			// nonce cookie 丢失、Casdoor 拒绝授权，还是组织不匹配。
			log.Printf("❌ Casdoor SSO 回调失败: %v", err)
			// 带原因跳回登录页（前端读取 ?sso_error= 展示），避免停留在裸 JSON 页面。
			http.Redirect(w, r, "/?sso_error="+url.QueryEscape(ssoFailMessage(err)), http.StatusFound)
			return
		}
		auditAnon(s, r, audit.ActionLogin, u.Username, "auth", "", nil)
		cd.SetSession(w, token)
		// 会话已写入 cookie，重定向回前端首页。
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// ssoFailMessage 把回调错误转成可在登录页展示的一句话。
//
// 仅包含 OAuth 阶段的原因（如 nonce 缺失、Casdoor 返回码、组织不匹配），不含任何密钥。
func ssoFailMessage(err error) string {
	msg := "未知错误"
	if err != nil {
		if s := strings.TrimSpace(err.Error()); s != "" {
			msg = s
		}
	}
	return "SSO 登录失败: " + msg
}

// ChangePassword 修改当前用户密码（自助）。
func ChangePassword(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 仅 builtin 支持密码修改
		b, ok := s.Auth.(*auth.Builtin)
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "当前认证方式不支持密码修改")
			return
		}

		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}

		var req struct {
			OldPassword string `json:"old_password"`
			NewPassword string `json:"new_password"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}

		token, err := b.ChangePassword(r.Context(), u.ID, req.OldPassword, req.NewPassword)
		if err != nil {
			action := audit.ActionPasswordChange
			auditOp(s, r, action, "auth", u.ID, u.Username, err)
			if err == auth.ErrWrongPassword {
				httpx.Fail(w, http.StatusBadRequest, "原密码错误")
				return
			}
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}

		auditOp(s, r, audit.ActionPasswordChange, "auth", u.ID, u.Username, nil)
		b.SetSession(w, token)
		httpx.OK(w, map[string]any{"message": "密码修改成功"})
	}
}
