package casdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// CreateUserInput 创建 Casdoor 用户入参。
//
// 注意：不包含 Properties —— 本系统的扩展字段只从 FlyIAM 同步，绝不回写
// Casdoor，避免与 FlyIAM 的数据源同步互相覆盖。
type CreateUserInput struct {
	Name        string
	DisplayName string
	Email       string
	Phone       string
	Avatar      string
	Password    string
}

// CreateUser 在 Casdoor 创建用户。
func (d *Directory) CreateUser(ctx context.Context, in CreateUserInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return fmt.Errorf("域账号不能为空")
	}
	body := map[string]any{
		"owner":             d.organization,
		"name":              name,
		"displayName":       strings.TrimSpace(in.DisplayName),
		"email":             strings.TrimSpace(in.Email),
		"phone":             strings.TrimSpace(in.Phone),
		"avatar":            strings.TrimSpace(in.Avatar),
		"password":          in.Password,
		"type":              "normal-user",
		"language":          "zh",
		"tag":               "manual",
		"address":           []string{},
		"signupApplication": d.application,
	}
	return d.postJSON(ctx, "add-user", d.organization+"/"+name, body)
}

// UpdateProfile 更新用户基础资料（姓名/邮箱/电话/头像）。
//
// 只修改上述字段，其余字段（含 properties）原样回传，避免丢失 FlyIAM
// 同步来的人事属性；本系统不会主动写入 properties。
func (d *Directory) UpdateProfile(ctx context.Context, name, display, email, phone, avatar string) error {
	raw, err := d.getRaw(ctx, name)
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("Casdoor 中不存在用户 %s", name)
	}
	raw["displayName"] = strings.TrimSpace(display)
	raw["email"] = strings.TrimSpace(email)
	raw["phone"] = strings.TrimSpace(phone)
	raw["avatar"] = strings.TrimSpace(avatar)
	return d.postJSON(ctx, "update-user", d.organization+"/"+name, raw)
}

// SetAdmin 设置/取消 Casdoor 管理员标记。
func (d *Directory) SetAdmin(ctx context.Context, name string, isAdmin bool) error {
	raw, err := d.getRaw(ctx, name)
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("Casdoor 中不存在用户 %s", name)
	}
	raw["isAdmin"] = isAdmin
	return d.postJSON(ctx, "update-user", d.organization+"/"+name, raw)
}

// SetPassword 管理员重置用户密码（oldPassword 为空）。
func (d *Directory) SetPassword(ctx context.Context, name, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("新密码不能为空")
	}
	form := url.Values{}
	form.Set("userOwner", d.organization)
	form.Set("userName", name)
	form.Set("oldPassword", "")
	form.Set("newPassword", newPassword)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.endpoint+"/api/set-password", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(d.clientID, d.clientSecret)
	return d.doJSON(req)
}

// getRaw 读取用户原始 JSON（保留所有字段，供更新时原样回传）。
func (d *Directory) getRaw(ctx context.Context, name string) (map[string]any, error) {
	if !d.Valid() {
		return nil, fmt.Errorf("Casdoor 未配置（endpoint/client_id/client_secret）")
	}
	q := url.Values{}
	q.Set("id", d.organization+"/"+strings.TrimSpace(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.endpoint+"/api/get-user?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(d.clientID, d.clientSecret)

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Casdoor 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Casdoor 返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var env struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("解析 Casdoor 响应失败: %s", snippet(body))
	}
	if env.Status != "" && env.Status != "ok" {
		return nil, fmt.Errorf("Casdoor: %s", env.Msg)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return nil, fmt.Errorf("解析 Casdoor 用户失败: %w", err)
	}
	return raw, nil
}

// postJSON 以 JSON 方式 POST 到 Casdoor 写接口。
func (d *Directory) postJSON(ctx context.Context, action, id string, payload any) error {
	if !d.Valid() {
		return fmt.Errorf("Casdoor 未配置（endpoint/client_id/client_secret）")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("id", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.endpoint+"/api/"+action+"?"+q.Encode(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(d.clientID, d.clientSecret)
	return d.doJSON(req)
}

// doJSON 发送请求并校验 Casdoor 的 {status,msg} 响应。
func (d *Directory) doJSON(req *http.Request) error {
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Casdoor 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Casdoor 返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var env struct {
		Status string `json:"status"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("解析 Casdoor 响应失败（疑似非 Casdoor 地址）: %s", snippet(body))
	}
	if env.Status != "" && env.Status != "ok" {
		return fmt.Errorf("Casdoor: %s", env.Msg)
	}
	return nil
}
