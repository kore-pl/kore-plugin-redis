# kore-plugin-redis（Redis 管理插件）

## 它做什么

真实连接用户已有的 Redis 实例，提供实例探测、信息读取、键浏览、取值 / 写入 / TTL 管理、删除，以及持久化 / 内存 / 慢查询 / 单键详情四类只读运维与白名单化的服务端配置调整。**不内置 Redis 服务、不捆绑 redis-server、不引入 Redis 客户端库**（G-05 / G-17）。

## 设计约束

- 客户端是 `internal/redisop` 内约 200 行的最小 RESP 实现（`net.Dial` + CRLF 编解码）；不依赖宿主机 `redis-cli`。
- 短连接：每次方法调用建立一条 TCP 连接，调用结束即关闭；无常驻连接、无后台轮询、无 goroutine 泄漏。
- 协议 `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`；16 个业务方法 + config 两条全部声明方法级能力（只读 `system:read`，写类 `system:admin`），stdout 留给协议帧，日志写 stderr。

## 方法

| 方法 | 入参 | 行为 |
| --- | --- | --- |
| `redis.instances` | `{}` | 探测配置中的实例，返回 name/host/port/reachable/version/uptime。 |
| `redis.info` | `instance?` | server/clients/memory/stats/persistence/keyspace 概要。 |
| `redis.keys` | `instance?`、`pattern?`、`limit?` | SCAN 浏览键，返回 type/ttl 与截断标记。 |
| `redis.get` | `instance?`、`key` | 读取键值与类型、TTL。 |
| `redis.set` | `instance?`、`key`、`type?`、`value`、`ttl_s?` | 写入键值，可指定类型与 TTL。 |
| `redis.delete` | `instance?`、`keys[]` | 删除指定键。 |
| `redis.ttl` / `redis.expire` | `instance?`、`key`、`ttl_s?` | 读 TTL / 设置过期时间。 |
| `redis.persistence` / `redis.memory` / `redis.slowlog` / `redis.keyInfo` | `instance?`、`key?`、`limit?` | 持久化、内存、慢查询与单键详情四类只读运维。 |

## 安全边界

凭据由环境变量 / 配置注入，不写日志、不进 README；连接目标受实例白名单约束，服务端配置调整只允许白名单项。

## 安装与验证

从插件市场安装 `redis`，或通过 `make dist` 产出 `dist/redis-0.6.0.tar.gz` 导入。开发验证：`make check`（gofmt → go vet → go test -timeout 200s），含 boundary / fault 测试。
