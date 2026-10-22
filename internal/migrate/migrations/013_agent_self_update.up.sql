-- Agent 自升级支持
--
-- agents.build_*：注册/心跳上报的构建信息。BuildHash 为 Agent 自身二进制的
-- SHA256，服务端据此判断是否已经是最新版本；旧 Agent 不上报，保持空值。
-- hosts.agent_build_*：冗余到主机列表，避免列表查询关联 agents 表。
-- 唯一索引：同一 Agent 同时只允许一条未结束的 self_update 指令，防止重复下发
-- 导致 Agent 在下载/替换阶段被并发覆盖。

ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS build_version VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS build_hash    VARCHAR(64)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS build_variant VARCHAR(32)  NOT NULL DEFAULT '';

ALTER TABLE hosts
    ADD COLUMN IF NOT EXISTS agent_build_version VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS agent_build_hash    VARCHAR(64)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS agent_build_variant VARCHAR(32)  NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_commands_agent_type_id
    ON commands(agent_id, type, id DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_commands_agent_active_self_update
    ON commands(agent_id)
    WHERE type = 'self_update'
      AND status IN ('pending', 'dispatched', 'accepted', 'running');
