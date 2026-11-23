# Changelog

## 0.6.0 (2026-11-23)

- feat: `redis.rename` —— 键重命名，走 `RENAME` / `RENAMENX`；**默认 `nx=true` 防覆盖**，目标已存在时返回 `renamed=false` + `target_existed=true` + 人话 hint；要覆盖必须显式 `nx:false` **且** `confirm:true`
- feat: `redis.move` —— 同实例跨 db 迁移（`MOVE`）；目标库已有同名键或源键不存在时按 Redis 语义返回 `moved=false` + hint（不覆盖、不踢键）
- feat: `redis.copy` —— 键复制（`COPY`，可选 `db` 跨库、`replace` 覆盖）；**默认不覆盖**，`replace:true` 必须同时 `confirm:true`，`dest_db=-1` 表示同库复制
- security: 三个方法一律 `system:admin`；**拒绝跨实例搬运**（不开放 `MIGRATE`，错误信息指引用 `redis.get` 读值写入或导出导入）
- security: 大键保护——`MEMORY USAGE` 超过 8 MiB 时 `rename` / `copy` 必须 `confirm:true` 并提示可能阻塞（大小取不到时放行，老版本 Redis 无 `MEMORY USAGE`）
- test: nx 默认语义、覆盖需 confirm、moved=false/copied=false 结果、大键门控（含大小未知放行）、参数校验（空键/同名/非法 db）、跨实例拒绝、方法级能力一致性（21 条）

## 0.5.1 (2026-11-23)

- feat: 凭据优先读内核注入的 `REDISCLI_AUTH`（ADR-0026 / §3.5 #8）——默认实例在内核注入该变量时**直接用、不再读插件 config 口令**；变量缺失时回落 `password` → `password_env`，老部署零改动
- feat: `redis.instances` 每项新增只读诊断 `credential_source ∈ kernel_env | plugin_config | none`（不含任何口令内容），用于回答「这次连的是哪把钥匙」
- security: 内核注入只作用于默认实例——多实例的其它实例各有自己的 config 口令，绝不被默认实例凭据覆盖；口令仍只经 `AUTH` 传输，不落日志、不回显、不落盘，`config.get` 口令字段继续打码
- test: 内核 env 存在时 config 口令零读取（可计数 seam）、缺失回落 `password` / `password_env`、三态、非默认实例不继承内核凭据、连接路径零读 config、`instances` 响应无明文断言

## 0.5.0 (2026-11-23)

- feat: **方法级能力声明** `info.json.method_capabilities` 共 18 条（16 业务 + `config.get` / `config.set`）——只读 11 个方法声明 `system:read`（`config.get` 在内），写类 7 个声明 `system:admin`（`config.set` 在内）。未声明的方法会回退插件级 `system:admin`，导致"看个配置也进 admin 审计"（PLUGIN-MATURITY §3.5 #12）
- feat: `redis.set` —— 写入/编辑键值：string→`SET`（`ttl_s` 用 `EX` 原子生效）、hash→`HSET`、list→`RPUSH`、set→`SADD`、zset→`ZADD`；`type` 省略时沿用键既有类型（键不存在要求显式类型，类型冲突 `E_INVALID`）；返回 `{key,type,written,ttl_s}`。单值 ≤1 MiB、单次 ≤1000 元素且总载荷 ≤4 MiB
- feat: `redis.ttl` —— 只读 TYPE+TTL（-1 永久 / 0 即将过期 / >0 剩余秒 + `persistent` / `expires_at`）；键不存在或并发删除 `E_NOTFOUND`
- feat: `redis.expire` —— 改 TTL，只接受正整数秒（上限 10 年），0/负数/超限 `E_INVALID` 并指向 `redis.persist` / `redis.ttl`；键不存在 `E_NOTFOUND`
- feat: `redis.persist` —— 去掉 TTL（去 TTL 的唯一入口，避免 `EXPIRE 0` 二义性）；本来无 TTL 时 `persisted=false`
- feat: `redis.bgsave` —— 后台 RDB 落盘（只发 `BGSAVE`，**不提供阻塞式 `SAVE`**）；已有落盘任务时 `E_UPSTREAM` + 稍后重试提示
- feat: `redis.serverConfig.get` / `redis.serverConfig.set` —— 服务端参数只开放白名单三项：`maxmemory` / `maxmemory-policy` / `timeout`；set 后回读实际生效值并列出 `changed`；`dir` / `dbfilename` / `appendonly` / `save` / `requirepass` 等返回 `E_UNSUPPORTED` + 原因
- feat: 配置契约补 `db`（连接后 `SELECT`，默认 0）与 `timeout_s`（建连与单次命令超时 1–30 秒，默认 5），连接级设置对全部实例生效
- security: **三层命令白名单**（`internal/redisop/whitelist.go`）：方法面只注册 16 个业务方法、无任意命令/脚本入口；参数面新增方法严格解码拒绝未知字段（夹带 `command`/`argv`/`script` 显式 `E_INVALID`）；传输面每条 RESP 命令发出前校验命令名与危险子命令，白名单外本地拒绝（`FLUSHALL` / `FLUSHDB` / `EVAL` / `EVALSHA` / `SCRIPT` / `FUNCTION` / `SHUTDOWN` / `KEYS` / `SAVE` / `DEBUG` / `MONITOR` / `ACL` / `MODULE` / `CLUSTER` / `MIGRATE` / `SWAPDB` / `SLAVEOF` / `REPLICAOF`），错误信息说明原因与替代做法
- test: capability 声明 vs 实现双向断言（表驱动，18 条 + `config.get`/`config.set` 单独断言 + manifest 同步）；TTL 三态边界（-1/0/正数）与非法 TTL；写入成功/失败/类型冲突/上限；危险命令与危险参数拒绝（含「命令未到达假服务端」的传输面证明）；`db`/`timeout_s` 校验与传播；故障注入矩阵从 9 方法扩到 16 方法
- docs: README 协议面补 7 个新方法契约、配置表补 `db`/`timeout_s`、安全章节改写为三层白名单、故障矩阵同步为 16 方法

## 0.4.0 (2026-11-23)

- feat: `redis.persistence` —— 持久化状态（`INFO persistence`）：`aof_enabled` / `aof_last_write_status` / `aof_size`（映射 `aof_current_size`）/ `rdb_last_save_time` / `rdb_changes_since_save` / `rdb_bgsave_in_progress`
- feat: `redis.memory` —— 内存策略（`INFO memory` + `INFO stats` + `CONFIG GET maxmemory-policy`）：`used` / `peak` / `maxmemory` / `maxmemory_policy`（八种淘汰策略枚举原样透传）/ `fragmentation_ratio` / `eviction_keys`；实例禁用 `CONFIG` 时策略降级空串不失败
- feat: `redis.slowlog` —— 慢查询（`SLOWLOG GET <limit>`，默认 50 / 上限 1000）：`entries[{id,ts,duration_us,command,args_summary}]`；单个参数按字符截断 128 加 `…`、最多 16 个参数（防大值刷屏），形状异常条目跳过
- feat: `redis.keyInfo` —— 单键详情（`TYPE`/`TTL`/`MEMORY USAGE`/`OBJECT ENCODING` 单条 pipeline，O(1)）：替代前端经 `redis.get` 盲取值的用法；Redis < 4.0 降级 `memory_bytes=0`、`encoding=""`；全程不使用 DEBUG 系列命令
- test: INFO persistence/memory 真实格式解析、CONFIG GET 扁平数组取值、八种策略枚举透传、SLOWLOG 条目解析与大值截断（128 字符 + 多字节）、keyInfo pipeline 顺序 / 缺键 E_NOTFOUND / 老版本降级单测；故障注入矩阵从 5 方法扩到 9 方法
- docs: README 协议面补四方法契约与降级语义

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
