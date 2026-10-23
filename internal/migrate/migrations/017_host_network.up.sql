-- 多网卡出口 IP 与隧道地址
--
-- 背景：主机常有多块网卡（业务网/管理网/隧道网），服务端观察到的来源地址
-- 可能不是主机实际出口地址；部分主机只能通过隧道访问平台。
--
-- hosts.ip        保持为「注册时服务端观察到的来源地址」，作为主机身份（唯一键）。
--                 Agent 走隧道注册时，该地址自然就是隧道地址。
-- hosts.exit_ip   Agent 用 UDP 路由探测到的实际出口 IP（多网卡时选择路由出口网卡），
--                 仅用于展示、搜索与任务 IP 段匹配。
-- hosts.tunnel_ip 运维手工填写的主机侧隧道 IP（展示/筛选，平台不反连主机）。
-- hosts.server_tunnel_url 运维手工填写的「连接地址覆盖」（迁移 019 重命名为
--                 server_url_override）：非空时服务端在心跳响应中下发，Agent
--                 自动切换并持久化。它不参与隧道建立，仅切换 Agent 的连接入口；
--                 清空后 Agent 下一次心跳回退安装时的默认地址。
-- hosts.agent_server_url  Agent 最近一次心跳自报的「当前实际连接的平台地址」，
--                 用于确认隧道切换是否已生效。
-- hosts.agent_default_server_url Agent 安装时的默认平台地址，服务端据此补齐
--                 隧道地址缺失的协议与端口（只填 IP 时沿用默认端口）。

ALTER TABLE hosts
    ADD COLUMN IF NOT EXISTS exit_ip VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tunnel_ip VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS server_tunnel_url VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS agent_server_url VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS agent_default_server_url VARCHAR(512) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_hosts_exit_ip   ON hosts(exit_ip);
CREATE INDEX IF NOT EXISTS idx_hosts_tunnel_ip ON hosts(tunnel_ip);
