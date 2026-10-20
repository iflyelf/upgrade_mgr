// Package notify 实现纯 Go 的 systemd sd_notify 协议（无 CGO、无外部依赖）。
//
// 用于：
//   - 启动就绪通知（READY=1），配合 systemd Type=notify；
//   - 看门狗心跳（WATCHDOG=1），配合 unit 的 WatchdogSec，实现「僵死」检测：
//     Agent 主循环卡死不再发送心跳时，systemd 会 kill 并按 Restart 策略拉起。
//
// 非 systemd 环境（无 NOTIFY_SOCKET）下所有调用均为无害空操作。
package notify

import (
	"net"
	"os"
	"strconv"
	"time"
)

// Notifier 封装到 systemd 通知套接字的连接。
type Notifier struct {
	conn *net.UnixConn
}

// New 依据 NOTIFY_SOCKET 建立通知连接；未在 systemd 下运行时返回 (nil, nil)。
func New() (*Notifier, error) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return nil, nil // 非 systemd 环境：空操作
	}
	// 抽象命名空间以 '@' 开头，Go 中用前导 '\0' 表示
	addr := &net.UnixAddr{Name: sock, Net: "unixgram"}
	if len(sock) > 0 && sock[0] == '@' {
		addr.Name = "\x00" + sock[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return nil, err
	}
	return &Notifier{conn: conn}, nil
}

// send 发送一条状态；n 为 nil 时安全跳过。
func (n *Notifier) send(state string) error {
	if n == nil || n.conn == nil {
		return nil
	}
	_, err := n.conn.Write([]byte(state))
	return err
}

// Ready 通知 systemd 服务已就绪（Type=notify）。
func (n *Notifier) Ready() error { return n.send("READY=1") }

// Watchdog 发送一次看门狗心跳。
func (n *Notifier) Watchdog() error { return n.send("WATCHDOG=1") }

// Status 更新 systemd 中显示的状态文本（systemctl status 可见）。
func (n *Notifier) Status(s string) error { return n.send("STATUS=" + s) }

// Reloading / Stopping 生命周期通知。
func (n *Notifier) Reloading() error { return n.send("RELOADING=1") }
func (n *Notifier) Stopping() error  { return n.send("STOPPING=1") }

// Close 关闭连接。
func (n *Notifier) Close() error {
	if n == nil || n.conn == nil {
		return nil
	}
	return n.conn.Close()
}

// WatchdogInterval 返回建议的心跳发送间隔（WatchdogSec 的一半）。
//
// 读取 systemd 注入的 WATCHDOG_USEC；未启用看门狗时返回 0。
func WatchdogInterval() time.Duration {
	usecStr := os.Getenv("WATCHDOG_USEC")
	if usecStr == "" {
		return 0
	}
	usec, err := strconv.ParseInt(usecStr, 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	// 若配置了 WATCHDOG_PID，需与本进程匹配才生效
	if pidStr := os.Getenv("WATCHDOG_PID"); pidStr != "" {
		if pid, err := strconv.Atoi(pidStr); err == nil && pid != os.Getpid() {
			return 0
		}
	}
	return time.Duration(usec) * time.Microsecond / 2
}
