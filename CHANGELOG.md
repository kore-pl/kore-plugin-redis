# Changelog

## 0.3.0 (2026-11-23)

- feat: 配置契约新增敏感字段 `password`（只存本机 0600 文件，`config.get` 只回 `***`，`config.set` 收到 `***` 保留原值）；主实例直连密码优先于 `password_env`，旧环境变量路径继续兼容
- test: 密码掩码/占位保留与直连密码优先的环境变量回退单测
- build: 位级可复现打包（stage chmod + tar --mode 归一、size_bytes 收敛），二进制改嵌 manifest.json，消除同版本 sha 漂移

## 0.2.0 (2026-11-23)

- feat: 真实连接 Redis（去 `noopExecutor`）——标准库最小 RESP 客户端，支持 `AUTH`、`PING`、`INFO`、`SCAN`、`TYPE`、`TTL`、`GET`、`HGETALL`、`LRANGE`、`SMEMBERS`、`ZRANGE`、`DEL`；不引入 Redis 客户端库，不依赖 `redis-cli`
- feat: `redis.instances` 对每个实例 PING + INFO server，返回 `reachable/version/uptime_s/error`
- feat: `redis.info` 解析 INFO 全量为 `server/clients/memory/stats/persistence/keyspace` 显式结构
- feat: `redis.keys` 用 SCAN 游标遍历（不用阻塞式 KEYS），逐键 TYPE/TTL；`limit` 默认 200、`truncated` 标记截断
- feat: `redis.get` 按类型分派读取（string/hash/list/set/zset），集合类封顶 100 项；缺失键返回 `E_NOTFOUND`
- feat: `redis.delete` 单次最多 100 键，返回实际删除数
- feat: 统一插件配置契约（`redis.config.get` / `redis.config.set`）——主实例 `host/port/password_env` + 标量列表附加实例；0600 原子写、保存即生效、敏感值不落盘
- security: 密码只从环境变量读取，经 RESP `AUTH` 传输，不进 argv / 配置文件 / 日志 / 响应；上游错误回显密码时脱敏为 `***`
- security: 不提供 `FLUSHALL` / `FLUSHDB` / `CONFIG SET` / `SHUTDOWN` 等危险命令与任意命令入口
- test: RESP 编解码单测、假 RESP 服务端协议测试（AUTH/SCAN pipeline/类型分派/密码脱敏）、参数与实例校验单测、真实 docker E2E（真实插件二进制 + UDS 握手 + config.set + 五方法全流程）
- chore: 移除骨架期的 `redis.scan` / 内存策略 / 前缀校验参数（方法面按真实管理契约重建）

## 0.1.0 (2026-11-22)

- feat: 新建 redis 管理插件骨架，复用 kore.plugin.v1 握手与会话循环
- feat: redis.instances / redis.scan 方法级参数校验与结构化结果——内存策略枚举（allkeys-lru/noeviction 等 8 种）+ key 前缀字面前缀校验，管理用户已有 Redis，不内置 redis-server、内核零依赖
- feat: 四类故障注入测试（kill -9 / 超时 / 磁盘满 / cgroup 超限），验证方法级错误返回与无故障反证
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner + gzip -9n + chmod 归一）
