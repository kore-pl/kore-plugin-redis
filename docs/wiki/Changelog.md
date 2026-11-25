# 变更记录

本页摘要，完整历史见仓库 [CHANGELOG.md](https://github.com/kore-pl/kore-plugin-redis/blob/main/CHANGELOG.md)。

## v管理插件（2026-11-25）

- 开源工程标准化：新增七语言文档（`docs/DETAIL*.md`、`locale/*.json`）与 README 语言切换行。
- 安全文件：新增 `.github/dependabot.yml`、`.github/CODEOWNERS`、`SECURITY.en.md`，并在 GitHub 开启 vulnerability alerts / secret scanning / push protection（能否开启以仓库实际回读为准）。
- 工程规范：补充 Git Flow 分支模型与 SSH 签名 tag 约定，创建本地 `develop` 分支（不推送）。
- 本地配置 SSH 签名并打 `v管理插件` 签名 tag；历史 tag 不改动、不重签、不推送。
