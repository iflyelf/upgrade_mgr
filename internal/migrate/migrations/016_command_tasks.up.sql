-- 一次性命令/脚本任务
--
-- 任务不再只绑定“组件升级”：kind='command' 的任务直接携带一段 Shell 脚本，
-- 在选中的主机上执行（用于卸载旧监控、清理残留等非组件场景）。
-- 因此 component_id 允许为空，命令内容保存在 command 列。
--
-- Agent 能力（capabilities）让服务端在下发前判断 Agent 是否支持 run_command，
-- 避免旧 Agent 收到未知指令后静默丢弃、任务卡到租约超时。

ALTER TABLE upgrade_tasks
    ADD COLUMN IF NOT EXISTS kind VARCHAR(16) NOT NULL DEFAULT 'upgrade',
    ADD COLUMN IF NOT EXISTS command JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE upgrade_tasks ALTER COLUMN component_id DROP NOT NULL;

ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS capabilities VARCHAR(255) NOT NULL DEFAULT '';
