-- upgrade_mgr 数据库结构（幂等，可重复执行）
-- 由服务端启动时自动执行（internal/svc/schema.go），此文件为参考快照。

-- ============================================================
-- 应用设置（页面可配置，DB 优先 / env 兜底）
-- ============================================================
CREATE TABLE IF NOT EXISTS app_settings (
    key        VARCHAR(128) PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- 内置账号（provider=builtin 时使用；casdoor 模式下不使用）
-- ============================================================
CREATE TABLE IF NOT EXISTS local_users (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(100) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    display_name  VARCHAR(200) DEFAULT '',
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    disabled      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- 主机清单（事实来源；Consul 只是同步来源之一）
-- ============================================================
CREATE TABLE IF NOT EXISTS hosts (
    id                 BIGSERIAL PRIMARY KEY,
    ip                 VARCHAR(64) NOT NULL UNIQUE,
    hostname           VARCHAR(255) DEFAULT '',
    consul_node        VARCHAR(255) DEFAULT '',
    datacenter         VARCHAR(64) DEFAULT '',
    os_family          VARCHAR(64) DEFAULT '',   -- centos/ubuntu/debian/...
    os_version         VARCHAR(64) DEFAULT '',
    kernel             VARCHAR(128) DEFAULT '',
    arch               VARCHAR(32) DEFAULT '',   -- amd64/arm64/...
    -- 业务分组（优先取自 Consul ServiceMeta）
    account            VARCHAR(128) DEFAULT '',
    app_code           VARCHAR(128) DEFAULT '',
    biz_group          VARCHAR(255) DEFAULT '',
    department         VARCHAR(255) DEFAULT '',
    vendor             VARCHAR(128) DEFAULT '',
    os_type            VARCHAR(64) DEFAULT '',
    host_type          VARCHAR(64) DEFAULT '',
    tags               TEXT[] DEFAULT '{}',
    -- Agent 与保活
    agent_id           VARCHAR(64) DEFAULT '',
    agent_version      VARCHAR(64) DEFAULT '',
    agent_status       VARCHAR(32) DEFAULT 'unknown', -- online/upgrading/restarting/crash_loop/offline/unknown
    supervisor         VARCHAR(32) DEFAULT '',        -- systemd/upstart/cron/bare
    component_versions JSONB DEFAULT '{}',
    -- 维护窗口
    maintenance_until  TIMESTAMPTZ,
    maintenance_reason VARCHAR(255) DEFAULT '',
    last_stage         VARCHAR(64) DEFAULT '',
    -- 来源与心跳
    source             VARCHAR(16) NOT NULL DEFAULT 'consul', -- agent/manual/csv/consul
    last_seen          TIMESTAMPTZ,
    last_synced_at     TIMESTAMPTZ,
    created_at         TIMESTAMPTZ DEFAULT NOW(),
    updated_at         TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_hosts_app_code   ON hosts(app_code);
CREATE INDEX IF NOT EXISTS idx_hosts_biz_group  ON hosts(biz_group);
CREATE INDEX IF NOT EXISTS idx_hosts_os_family  ON hosts(os_family);
CREATE INDEX IF NOT EXISTS idx_hosts_arch       ON hosts(arch);
CREATE INDEX IF NOT EXISTS idx_hosts_agent_stat ON hosts(agent_status);
CREATE INDEX IF NOT EXISTS idx_hosts_source     ON hosts(source);

-- ============================================================
-- 组件定义
-- ============================================================
CREATE TABLE IF NOT EXISTS components (
    id             BIGSERIAL PRIMARY KEY,
    name           VARCHAR(128) NOT NULL UNIQUE,
    display_name   VARCHAR(200) DEFAULT '',
    deploy_mode    VARCHAR(24) NOT NULL DEFAULT 'replace', -- replace/exec/replace_exec
    binary_name    VARCHAR(128) DEFAULT '',
    install_path   VARCHAR(512) DEFAULT '',
    backup_dir     VARCHAR(512) DEFAULT '/var/lib/upgrade-agent/backup',
    exec_path      VARCHAR(512) DEFAULT '',
    exec_args      TEXT[] DEFAULT '{}',
    exec_as_user   VARCHAR(64) DEFAULT '',
    exec_timeout   VARCHAR(24) DEFAULT '10m',
    exec_env       TEXT[] DEFAULT '{}',
    work_dir       VARCHAR(512) DEFAULT '',
    ignore_error   BOOLEAN NOT NULL DEFAULT FALSE,
    pre_install    JSONB DEFAULT '[]',   -- [][]string
    post_install   JSONB DEFAULT '[]',
    service_name   VARCHAR(128) DEFAULT '',
    service_mgr    VARCHAR(24) DEFAULT 'auto', -- auto/systemd/sysv/none
    stop_cmd       JSONB DEFAULT '[]',
    start_cmd      JSONB DEFAULT '[]',
    restart_cmd    JSONB DEFAULT '[]',
    version_cmd    JSONB DEFAULT '[]',
    version_regex  VARCHAR(255) DEFAULT '',
    health_cmd     JSONB DEFAULT '[]',
    consul_maint   BOOLEAN NOT NULL DEFAULT FALSE,
    os_match       VARCHAR(255) DEFAULT '',
    arch_match     VARCHAR(255) DEFAULT '',
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ DEFAULT NOW(),
    updated_at     TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- 制品（组件 × 版本 × OS × ARCH）
-- ============================================================
CREATE TABLE IF NOT EXISTS artifacts (
    id              BIGSERIAL PRIMARY KEY,
    component_id    BIGINT NOT NULL REFERENCES components(id) ON DELETE CASCADE,
    version         VARCHAR(64) NOT NULL,
    os_family       VARCHAR(64) DEFAULT '',
    os_version_spec VARCHAR(64) DEFAULT '',
    arch            VARCHAR(32) NOT NULL,
    filename        VARCHAR(255) NOT NULL,
    size            BIGINT NOT NULL DEFAULT 0,
    md5             VARCHAR(32) NOT NULL,
    sha256          VARCHAR(64) DEFAULT '',
    storage         VARCHAR(16) NOT NULL DEFAULT 'local',
    path            VARCHAR(1024) NOT NULL,
    inner_path      VARCHAR(512) DEFAULT '',
    notes           TEXT DEFAULT '',
    status          VARCHAR(16) NOT NULL DEFAULT 'active',
    uploaded_by     VARCHAR(100) DEFAULT '',
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(component_id, version, arch, os_family)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_component ON artifacts(component_id);

-- ============================================================
-- 升级任务
-- ============================================================
CREATE TABLE IF NOT EXISTS upgrade_tasks (
    id           BIGSERIAL PRIMARY KEY,
    name         VARCHAR(200) NOT NULL,
    component_id BIGINT NOT NULL REFERENCES components(id) ON DELETE RESTRICT,
    version      VARCHAR(64) NOT NULL,
    selector     JSONB NOT NULL DEFAULT '{}',
    strategy     JSONB NOT NULL DEFAULT '{}',
    status       VARCHAR(24) NOT NULL DEFAULT 'draft', -- draft/running/paused/success/failed/rolled_back/canceled
    schedule_at  TIMESTAMPTZ,
    created_by   VARCHAR(100) DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tasks_status  ON upgrade_tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_created ON upgrade_tasks(created_at DESC);

CREATE TABLE IF NOT EXISTS task_batches (
    id          BIGSERIAL PRIMARY KEY,
    task_id     BIGINT NOT NULL REFERENCES upgrade_tasks(id) ON DELETE CASCADE,
    batch_index INT NOT NULL,
    status      VARCHAR(24) NOT NULL DEFAULT 'pending',
    total       INT NOT NULL DEFAULT 0,
    success     INT NOT NULL DEFAULT 0,
    failed      INT NOT NULL DEFAULT 0,
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    UNIQUE(task_id, batch_index)
);

CREATE TABLE IF NOT EXISTS task_hosts (
    id           BIGSERIAL PRIMARY KEY,
    task_id      BIGINT NOT NULL REFERENCES upgrade_tasks(id) ON DELETE CASCADE,
    host_id      BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    batch_index  INT NOT NULL DEFAULT 0,
    status       VARCHAR(24) NOT NULL DEFAULT 'pending', -- pending/running/success/failed/rolled_back/skipped/timeout
    stage        VARCHAR(32) DEFAULT '',
    from_version VARCHAR(64) DEFAULT '',
    to_version   VARCHAR(64) DEFAULT '',
    attempts     INT NOT NULL DEFAULT 0,
    exit_code    INT DEFAULT 0,
    log          TEXT DEFAULT '',
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    UNIQUE(task_id, host_id)
);
CREATE INDEX IF NOT EXISTS idx_task_hosts_task   ON task_hosts(task_id);
CREATE INDEX IF NOT EXISTS idx_task_hosts_status ON task_hosts(status);

-- ============================================================
-- Agent 与指令队列
-- ============================================================
CREATE TABLE IF NOT EXISTS agents (
    id                BIGSERIAL PRIMARY KEY,
    agent_id          VARCHAR(64) NOT NULL UNIQUE,
    host_id           BIGINT REFERENCES hosts(id) ON DELETE SET NULL,
    ip                VARCHAR(64) DEFAULT '',
    token_hash        VARCHAR(255) NOT NULL,
    secret            VARCHAR(255) NOT NULL DEFAULT '', -- 命令信封 HMAC 密钥
    arch              VARCHAR(32) DEFAULT '',
    os                VARCHAR(64) DEFAULT '',
    agent_version     VARCHAR(64) DEFAULT '',
    supervisor        VARCHAR(32) DEFAULT '',
    status            VARCHAR(24) NOT NULL DEFAULT 'offline',
    restart_count     INT NOT NULL DEFAULT 0,
    last_restart_at   TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_agents_host ON agents(host_id);

CREATE TABLE IF NOT EXISTS commands (
    id                  BIGSERIAL PRIMARY KEY,
    task_id             BIGINT REFERENCES upgrade_tasks(id) ON DELETE CASCADE,
    task_host_id        BIGINT REFERENCES task_hosts(id) ON DELETE CASCADE,
    host_id             BIGINT REFERENCES hosts(id) ON DELETE CASCADE,
    agent_id            VARCHAR(64) NOT NULL,
    type                VARCHAR(32) NOT NULL,
    payload             JSONB NOT NULL DEFAULT '{}',
    status              VARCHAR(24) NOT NULL DEFAULT 'pending', -- pending/dispatched/accepted/running/success/failed/rolled_back/timeout/expired/canceled
    attempts            INT NOT NULL DEFAULT 0,
    maintenance_seconds INT NOT NULL DEFAULT 0,
    will_restart        BOOLEAN NOT NULL DEFAULT FALSE,
    lease_expire_at     TIMESTAMPTZ,
    expire_at           TIMESTAMPTZ,
    result              JSONB,
    log                 TEXT DEFAULT '',
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    dispatched_at       TIMESTAMPTZ,
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_commands_agent_status ON commands(agent_id, status);
CREATE INDEX IF NOT EXISTS idx_commands_task         ON commands(task_id);

-- ============================================================
-- 审计日志
-- ============================================================
CREATE TABLE IF NOT EXISTS audit_logs (
    id            BIGSERIAL PRIMARY KEY,
    user_id       VARCHAR(100),
    username      VARCHAR(100),
    action        VARCHAR(64),
    resource_type VARCHAR(64),
    resource_id   VARCHAR(100),
    resource_name VARCHAR(255),
    details       JSONB,
    ip_address    VARCHAR(64),
    status        VARCHAR(24),
    error_message TEXT,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user    ON audit_logs(user_id);
