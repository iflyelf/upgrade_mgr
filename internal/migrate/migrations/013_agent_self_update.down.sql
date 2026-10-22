-- 回滚 Agent 自升级支持

DROP INDEX IF EXISTS uq_commands_agent_active_self_update;
DROP INDEX IF EXISTS idx_commands_agent_type_id;

ALTER TABLE hosts
    DROP COLUMN IF EXISTS agent_build_version,
    DROP COLUMN IF EXISTS agent_build_hash,
    DROP COLUMN IF EXISTS agent_build_variant;

ALTER TABLE agents
    DROP COLUMN IF EXISTS build_version,
    DROP COLUMN IF EXISTS build_hash,
    DROP COLUMN IF EXISTS build_variant;
