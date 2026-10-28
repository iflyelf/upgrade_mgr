-- 任务批量删除异步作业：删除在服务端后台按批执行，页面刷新/切换页签不会中断。
-- 进度（processed/deleted/failed）与每批删除在同一事务内提交，服务重启后从已提交进度继续。
CREATE TABLE IF NOT EXISTS task_delete_jobs (
    id               BIGSERIAL PRIMARY KEY,
    tenant_id        INT NOT NULL DEFAULT 1 REFERENCES tenants(id),
    -- 发起人身份快照：后台执行时据此重建作用域与 owner 校验。
    cross_tenant     BOOLEAN NOT NULL DEFAULT FALSE,
    created_by       VARCHAR(100) NOT NULL DEFAULT '',
    actor_is_admin   BOOLEAN NOT NULL DEFAULT FALSE,
    actor_roles      TEXT[] NOT NULL DEFAULT '{}',
    actor_ip         VARCHAR(64) NOT NULL DEFAULT '',
    ids              BIGINT[] NOT NULL,
    total            INT NOT NULL DEFAULT 0,
    processed        INT NOT NULL DEFAULT 0,
    deleted          INT NOT NULL DEFAULT 0,
    failed_count     INT NOT NULL DEFAULT 0,
    -- 只保留前若干条失败明细，避免单行过大；总数见 failed_count。
    failed           JSONB NOT NULL DEFAULT '[]'::jsonb,
    status           VARCHAR(16) NOT NULL DEFAULT 'pending', -- pending/running/done/canceled
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_task_delete_jobs_active
    ON task_delete_jobs(id) WHERE status IN ('pending','running');
CREATE INDEX IF NOT EXISTS idx_task_delete_jobs_owner
    ON task_delete_jobs(tenant_id, created_by, id DESC);
-- 同一发起人同时只允许一个未结束作业（并发提交时由数据库兜底）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_task_delete_jobs_one_active
    ON task_delete_jobs(tenant_id, created_by) WHERE status IN ('pending','running');
