-- 指令进度与防重复执行
--
-- last_stage/last_stage_at：即使最终 report 丢失，任务详情和主机列表也能展示
--   Agent 最后执行到哪一步；对账逻辑据此判断是否已经到达终态阶段。
-- uq_commands_task_host_active：同一 task_host 同时只允许一条活动指令，
--   防止编排器重试/多副本并发时重复下发、重复升级。
-- idx_commands_active_lease：租约回收扫描。

ALTER TABLE commands
    ADD COLUMN IF NOT EXISTS last_stage    VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_stage_at TIMESTAMPTZ;

-- 历史数据回填：优先取 result.stage，其次取 task_hosts.stage。
UPDATE commands c
SET last_stage = COALESCE(NULLIF(c.result->>'stage',''), NULLIF(th.stage,''), '')
FROM task_hosts th
WHERE c.task_host_id = th.id
  AND c.last_stage = '';

-- 所有历史指令都回填，便于对账逻辑识别「阶段已 done 但 report 丢失」的卡单。
UPDATE commands
SET last_stage_at = COALESCE(finished_at, started_at, dispatched_at, created_at)
WHERE last_stage_at IS NULL;

-- 清理历史重复活动指令：同一 task_host 只保留 id 最小的一条，其余取消。
-- 先清理再建唯一索引，避免生产库已有重复数据导致迁移失败。
WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY task_host_id ORDER BY id) AS rn
    FROM commands
    WHERE task_host_id IS NOT NULL
      AND status IN ('pending','dispatched','accepted','running')
)
UPDATE commands c
SET status='canceled', finished_at=NOW(),
    log=COALESCE(c.log,'')||'[migration] 检测到同一主机的重复活动指令，已取消\n'
FROM ranked r
WHERE c.id = r.id AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS uq_commands_task_host_active
    ON commands(task_host_id)
    WHERE task_host_id IS NOT NULL
      AND status IN ('pending','dispatched','accepted','running');

CREATE INDEX IF NOT EXISTS idx_commands_active_lease
    ON commands(lease_expire_at)
    WHERE status IN ('dispatched','accepted','running');

CREATE INDEX IF NOT EXISTS idx_commands_active_stage
    ON commands(last_stage, last_stage_at)
    WHERE status IN ('dispatched','accepted','running');

-- 对账时按 task_host 取最新终态指令；避免大表在每轮编排时全量扫描。
CREATE INDEX IF NOT EXISTS idx_commands_task_host_id
    ON commands(task_host_id, id DESC)
    WHERE task_host_id IS NOT NULL;
