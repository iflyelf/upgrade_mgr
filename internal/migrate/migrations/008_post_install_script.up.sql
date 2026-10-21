-- 组件的 post_install 脚本内容。
--
-- 与 post_install（[][]string 命令数组）并存：脚本内容由 Agent 写入临时文件后
-- 用 bash/sh 执行，适合较长、带流程控制的安装后逻辑，避免在 JSON 里转义大段 shell。
ALTER TABLE components ADD COLUMN IF NOT EXISTS post_install_script TEXT NOT NULL DEFAULT '';
