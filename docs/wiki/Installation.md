# 安装

## 从插件市场安装（推荐）

在 kore 管理界面打开插件市场，搜索 `redis`，选择版本 `v管理插件` 安装并启用。启用时确认 `info.json` 中声明的能力。

## 手动安装

0.6.1

## 前置条件

- kore 内核满足 `info.json.requires_kore` 声明的版本区间。
- 插件以独立进程运行，socket 由主壳创建、`chown kore-<id>`、权限 `0600`。
- 开发验证用 `make check`（gofmt → go vet → `go test -timeout 200s`），发布前 `make dist` 连跑两次核对 sha256 一致。

## 升级与回滚

升级时先备份当前 `<state>` 目录，安装新版本后跑一次 `make check` 或管理界面的健康检查。回滚使用上一版本的 dist 包重装即可；配置格式若变化，参见 [变更记录](Changelog)。
