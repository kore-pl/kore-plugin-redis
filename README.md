# kore-plugin-redis

Redis 管理插件（**真实连接**）：连接用户已有的 Redis 实例，提供实例探测、信息读取、键浏览、取值与删除，以及持久化 / 内存 / 慢查询 / 单键详情四类只读运维。不内置 Redis 服务、不捆绑 `redis-server`、不引入 Redis 客户端库（G-05 无中间件 / G-17 只依赖 `kore/pkg/pluginapi`）。

## 定位与边界

- **管理用户已有的 Redis**：插件只连接、读取与管理键，不安装、不启动、不停止 Redis 服务。
- **零第三方依赖**：只依赖 `kore/pkg/pluginapi` 与 Go 标准库；客户端是 `internal/redisop` 内约 200 行的最小 RESP 实现（`net.Dial` + CRLF 编解码）。
- **不依赖 `redis-cli`**：宿主机没有 `redis-cli` 也能工作；E2E 中仅用容器内 `redis-cli` 造测试数据。
- **短连接**：每次方法调用建立一条 TCP 连接，调用结束即关闭；无常驻连接、无后台轮询、无 goroutine 泄漏。

## 协议面

`kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`，能力 `system:admin`；stdout 留给协议帧，日志一律写 stderr。

| 方法 | 入参 | 结果 |
| --- | --- | --- |
| `redis.instances` | `{}` | `{instances:[{name,host,port,reachable,version,uptime_s,error}],count}` |
| `redis.info` | `{instance?}` | `{instance,server:{version,uptime_s,tcp_port},clients:{connected},memory:{used_memory_human,used_memory_peak_human,maxmemory_human},stats:{...},persistence:{rdb_last_save_time,aof_enabled},keyspace:{db0:"keys=1,expires=0"}}` |
| `redis.keys` | `{instance?,pattern?,limit?}` | `{keys:[{key,type,ttl_s}],scanned,truncated}` |
| `redis.get` | `{instance?,key}` | `{key,type,ttl_s,value,truncated?}` |
| `redis.delete` | `{instance?,keys[]}` | `{deleted,keys[]}` |
| `redis.persistence` | `{instance?}` | `{instance,aof_enabled,aof_last_write_status,aof_size,rdb_last_save_time,rdb_changes_since_save,rdb_bgsave_in_progress}` |
| `redis.memory` | `{instance?}` | `{instance,used,peak,maxmemory,maxmemory_policy,fragmentation_ratio,eviction_keys}` |
| `redis.slowlog` | `{instance?,limit?}` | `{entries:[{id,ts,duration_us,command,args_summary}],count}` |
| `redis.keyInfo` | `{instance?,key}` | `{key,type,encoding,ttl_s,memory_bytes}` |

- `instance` 在单实例时可省略；多实例时必须指定。未配置实例返回 `E_NOTFOUND`，未知实例返回 `E_NOTFOUND`，参数非法返回 `E_INVALID`。
- `redis.keys` 使用 **SCAN 游标遍历**，不使用阻塞式 `KEYS`；`pattern` 默认 `*`，`limit` 默认 200、上限 10000，超过上限即 `truncated=true`。
- `redis.get` 按 `TYPE` 分派：`string→GET`、`hash→HGETALL`、`list→LRANGE`、`set→SMEMBERS`、`zset→ZRANGE WITHSCORES`；list/set/zset/hash 最多返回 100 项并标记 `truncated`，`stream` 等未支持类型返回 `E_UNSUPPORTED`。
- `redis.delete` 一次最多 100 个键；重复键与超长键返回 `E_INVALID`。
- `redis.persistence` / `redis.memory` 只读：解析 `INFO persistence` / `INFO memory` + `INFO stats`；策略值经 `CONFIG GET maxmemory-policy` 读取（**不做 `CONFIG SET`**）；云托管实例禁用 `CONFIG` 时 `maxmemory_policy` 降级为空串。`eviction_keys` 来自 INFO stats 的 `evicted_keys`（实例启动以来被逐出的键总数）。
- `redis.slowlog` 对应 `SLOWLOG GET <limit>`：`limit` 默认 50、上限 1000；单个参数按**字符**截断到 128 加 `…`（防大值刷屏），最多保留 16 个参数，超出合并为 `…(+N)`；形状异常的条目跳过不失败。
- `redis.keyInfo` 用一条 pipeline 取 `TYPE` / `TTL` / `MEMORY USAGE` / `OBJECT ENCODING`，全程 O(1)，大键同样快；**不使用 DEBUG 系列命令**；`MEMORY USAGE` 需 Redis ≥ 4.0，老版本降级 `memory_bytes=0`、`encoding=""`。

## 配置契约（`redis.config.get` / `redis.config.set`）

遵循 `kore/docs/PLUGIN-CONFIG.md`：存储 `<KORE_STATE_DIR>/config.json`（0600 原子写），优先级 **config.json > 环境变量 > 代码默认值**，保存后同一进程内下一次调用立即生效（`restart_required=false`）。

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `host` | string | `127.0.0.1` | 主实例主机名/IP |
| `port` | int | `6379` | 主实例端口 |
| `password` | string（sensitive） | `""` | 主实例密码，只存本机 0600 文件；`config.get` 只回 `***` |
| `password_env` | string | `""` | 主实例密码所在环境变量名（不是密码值）；`password` 优先 |
| `instances` | list(string) | `[]` | 附加实例，每项 `<名称>=<主机>:<端口>` |
| `password_envs` | list(string) | `[]` | 附加实例密码环境变量，每项 `<名称>=<环境变量名>` |

示例：

```json
{
  "values": {
    "host": "127.0.0.1",
    "port": 6379,
    "password_env": "REDIS_PASSWORD",
    "instances": ["cache=10.0.0.5:6380"],
    "password_envs": ["cache=REDIS_CACHE_PASSWORD"]
  }
}
```

环境变量兜底（仅当配置文件不存在时生效）：`KORE_REDIS_HOST`、`KORE_REDIS_PORT`、`KORE_REDIS_PASSWORD_ENV`。

## 安全

- **密码两种来源**：界面填写的 `password` 只存本机 0600 配置文件，`config.get` 只回 `***`、日志与响应不回传；未填写时回退到 `password_env` 指向的环境变量（需在 kored / 插件进程环境中导出，如 systemd unit 的 `Environment=`）。
- **密码不进 argv**：插件不调用任何子进程客户端；密码通过 RESP `AUTH <password>` 在 TCP 连接内传输。
- **错误脱敏**：认证失败或上游错误信息若回显密码，会先替换为 `***` 再返回。
- **禁止危险命令**：本插件**不提供** `FLUSHALL` / `FLUSHDB` / `CONFIG SET` / `SHUTDOWN` 等危险命令，也不提供任意命令执行入口；方法面只注册上表 9 个业务方法，其余一律 `E_NOTFOUND`。
- **超时与限额**：连接超时 5s，命令超时 5s；`pattern` ≤256 字节，批量删除 ≤100 个键，单键 ≤1024 字节，RESP bulk ≤8 MiB。

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
```

`dist` 使用确定性参数（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` + chmod 归一），两次构建 sha256 一致；`size_bytes` 由打包脚本迭代回填，与实际包体偏差 ≤10%。

### 真实 E2E（docker）

```bash
docker run -d --name kore-redis-test -p 16379:6379 redis:7-alpine
KORE_REDIS_E2E=1 KORE_REDIS_E2E_PORT=16379 go test -timeout 180s -v ./tests/e2e
docker rm -f kore-redis-test
```

E2E 启动真实插件二进制，经 UDS 完成 `kore.hello`，再通过 `redis.config.set` 配置实例，依次验证：`instances` 可达与版本 → `info` 的 memory/clients/stats/persistence/keyspace → 容器内 `redis-cli SET foo bar` 造数 → `keys` 找到 `foo` → `get foo` 返回 `bar`（type=string）→ `delete [foo]` 返回 `deleted=1` → 再 `get` 返回 `E_NOTFOUND` → 危险命令 `E_NOTFOUND`。

## 四类故障注入

`tests/fault/` 对 kill -9、超时、磁盘满、cgroup 超限四类故障注入到执行面，断言 9 个业务方法返回结构化错误，且 `Error.Code` 与故障类别一一对应（`E_UPSTREAM` / `E_UPSTREAM` / `E_INTERNAL` / `E_UNSUPPORTED`），并带无故障 sanity 反证。
