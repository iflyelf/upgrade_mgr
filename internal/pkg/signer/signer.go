// Package signer 对 Agent 指令信封做 HMAC-SHA256 签名与校验，防伪造与重放。
//
// 服务端下发指令时用「每 Agent 唯一密钥」签名 (method|path|body|ts|nonce)，
// Agent 侧用同一密钥校验签名、时间窗与 nonce，拒绝过期或重放的指令。
package signer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Envelope 待签名/校验的信封要素。
type Envelope struct {
	Method    string
	Path      string
	Body      []byte
	Timestamp int64  // Unix 秒
	Nonce     string // 唯一随机串
}

// Sign 计算信封的 HMAC-SHA256 十六进制签名。
func Sign(secret string, e Envelope) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(e.Method))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(e.Path))
	mac.Write([]byte{'\n'})
	mac.Write(e.Body)
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strconv.FormatInt(e.Timestamp, 10)))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(e.Nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 校验签名、时间窗与非空 nonce。
//
// skew 为允许的时钟偏移窗口（含未来），建议 >= 5min 以容忍 NTP 漂移。
// nonce 去重由调用方（结合持久化/缓存）负责，本函数只校验非空。
func Verify(secret, signature string, e Envelope, skew time.Duration) error {
	if strings.TrimSpace(e.Nonce) == "" {
		return fmt.Errorf("signer: 缺少 nonce")
	}
	expected := Sign(secret, e)
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("signer: 签名不匹配")
	}
	now := time.Now().Unix()
	skewSec := int64(skew / time.Second)
	if e.Timestamp < now-skewSec || e.Timestamp > now+skewSec {
		return fmt.Errorf("signer: 时间戳超出允许窗口（±%s）", skew)
	}
	return nil
}
