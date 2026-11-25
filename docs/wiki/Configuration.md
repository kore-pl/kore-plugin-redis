# 配置

## 配置来源

插件配置由环境变量 / 注入的 UDS 会话提供；请求不携带宿主绝对路径。具体键与默认值见仓库 README 与 `info.json`。

## 能力声明

`info.json.capabilities` 是本插件的完整授权面，方法级 `method_capabilities` 只收窄、不放大。启用时应逐项确认。

## 资源限制

`info.json.resources` 给出 `memory_mb` / `cpu_quota` / `pids_max`，由主壳执行。插件内**不起 ticker / 轮询 / 周期任务**（G-19）：需要定时请交给内核 scheduler；调用级超时用 `context.WithTimeout` / `time.Timer` 并确保 `Stop()`。

## 日志与协议

stdout 留给 `kore.plugin.v1` 协议帧，日志一律写 stderr。握手为 `kore.hello`，之后进入会话循环。

## 安全

不要开公开 issue 报漏洞，按 [SECURITY.md](https://github.com/kore-pl/kore-plugin-redis/blob/main/SECURITY.md) 私下披露；英文见 [SECURITY.en.md](https://github.com/kore-pl/kore-plugin-redis/blob/main/SECURITY.en.md)。
