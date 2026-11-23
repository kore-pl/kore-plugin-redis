# kore-plugin-redis

Redis 管理插件（**真实连接**）：连接用户已有的 Redis 实例，提供实例探测、信息读取、键浏览、取值 / 写入 / TTL 管理、删除，以及持久化 / 内存 / 慢查询 / 单键详情四类只读运维与 **白名单化的服务端配置调整**。不内置 Redis 服务、不捆绑 `redis-server`、不引入 Redis 客户端库（G-05 无中间件 / G-17 只依赖 `kore/pkg/pluginapi`）。

## 定位与边界

- **管理用户已有的 Redis**：插件只连接、读取与管理键，不安装、不启动、不停止 Redis 服务。
- **零第三方依赖**：只依赖 `kore/pkg/pluginapi` 与 Go 标准库；客户端是 `internal/redisop` 内约 200 行的最小 RESP 实现（`net.Dial` + CRLF 编解码）。
- **不依赖 `redis-cli`**：宿主机没有 `redis-cli` 也能工作；E2E 中仅用容器内 `redis-cli` 造测试数据。
- **短连接**：每次方法调用建立一条 TCP 连接，调用结束即关闭；无常驻连接、无后台轮询、无 goroutine 泄漏。

## 协议面

`kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`；插件级能力 `system:admin` 只作为兜底，**16 个业务方法 + config 两条全部声明方法级能力**（只读 `system:read`、写类 `system:admin`，见 `info.json.method_capabilities`）；stdout 留给协议帧，日志一律写 stderr。

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
| `redis.set` | `{instance?,key,type?,value,ttl_s?}` | `{key,type,written,ttl_s}` |
| `redis.ttl` | `{instance?,key}` | `{key,type,ttl_s,persistent,expires_at}` |
| `redis.expire` | `{instance?,key,ttl_s}` | `{key,ttl_s}` |
| `redis.persist` | `{instance?,key}` | `{key,persisted,ttl_s}` |
| `redis.bgsave` | `{instance?}` | `{started,message}` |
| `redis.serverConfig.get` | `{instance?}` | `{instance,config:{maxmemory,maxmemory_policy,timeout}}` |
| `redis.serverConfig.set` | `{instance?,maxmemory?,maxmemory_policy?,timeout?}` | `{instance,changed[],config{...}}` |

- `instance` 在单实例时可省略；多实例时必须指定。未配置实例返回 `E_NOTFOUND`，未知实例返回 `E_NOTFOUND`，参数非法返回 `E_INVALID`。
- `redis.keys` 使用 **SCAN 游标遍历**，不使用阻塞式 `KEYS`；`pattern` 默认 `*`，`limit` 默认 200、上限 10000，超过上限即 `truncated=true`。
- `redis.get` 按 `TYPE` 分派：`string→GET`、`hash→HGETALL`、`list→LRANGE`、`set→SMEMBERS`、`zset→ZRANGE WITHSCORES`；list/set/zset/hash 最多返回 100 项并标记 `truncated`，`stream` 等未支持类型返回 `E_UNSUPPORTED`。
- `redis.delete` 一次最多 100 个键；重复键与超长键返回 `E_INVALID`。
- `redis.persistence` / `redis.memory` 只读：解析 `INFO persistence` / `INFO memory` + `INFO stats`；策略值经 `CONFIG GET maxmemory-policy` 读取（**不做 `CONFIG SET`**）；云托管实例禁用 `CONFIG` 时 `maxmemory_policy` 降级为空串。`eviction_keys` 来自 INFO stats 的 `evicted_keys`（实例启动以来被逐出的键总数）。
- `redis.slowlog` 对应 `SLOWLOG GET <limit>`：`limit` 默认 50、上限 1000；单个参数按**字符**截断到 128 加 `…`（防大值刷屏），最多保留 16 个参数，超出合并为 `…(+N)`；形状异常的条目跳过不失败。
- `redis.keyInfo` 用一条 pipeline 取 `TYPE` / `TTL` / `MEMORY USAGE` / `OBJECT ENCODING`，全程 O(1)，大键同样快；**不使用 DEBUG 系列命令**；`MEMORY USAGE` 需 Redis ≥ 4.0，老版本降级 `memory_bytes=0`、`encoding=""`。
- `redis.set` 是**写入**（upsert）：`type` 省略时沿用键既有类型，键不存在则要求显式类型；显式类型与既有类型不一致返回 `E_INVALID`（避免 WRONGTYPE 到写入后才暴露）。`value` 形状随类型：`string`→字符串、`hash`→`[{field,value}]`、`list`/`set`→字符串数组、`zset`→`[{member,score}]`（score 接受数字或字符串）。`hash` 走 `HSET`（追加/覆盖字段）、`list` 走 `RPUSH`、`set` 走 `SADD`、`zset` 走 `ZADD`；`written` 原样透传命令计数（string=1、list=写入后长度、集合类=新增元素数）。`ttl_s` 省略=不改 TTL、`>0`=同时设置过期（string 用 `SET ... EX` 原子生效，集合类写入后 `EXPIRE`），`0` 与负数显式拒绝（去 TTL 只能用 `redis.persist`）。
- `redis.ttl` 只读：`ttl_s` 原样透传 Redis 语义（`-1` 永久且 `persistent=true`、`0` 即将过期、`>0` 剩余秒），同时给出 `expires_at`（Unix 秒，非正数时为 0）；键不存在或 `TYPE`/`TTL` 之间被删除返回 `E_NOTFOUND`。
- `redis.expire` 只接受正整数秒（上限 10 年）：`0`/负数/超限返回 `E_INVALID` 并指向 `redis.persist` / `redis.ttl`；键不存在（`EXPIRE` 返回 0）返回 `E_NOTFOUND`。
- `redis.persist` 去掉 TTL：键不存在返回 `E_NOTFOUND`；本来就没有 TTL 时 `persisted=false`（不是错误）。
- `redis.bgsave` 只发送 `BGSAVE`（后台异步落盘）；已有落盘任务时返回 `E_UPSTREAM` 并提示稍后重试。**不提供阻塞式 `SAVE`**。
- `redis.serverConfig.get/set` 只操作三个白名单参数：`maxmemory`（字节或 kb/mb/gb）、`maxmemory_policy`（8 种淘汰策略枚举）、`timeout`（0–86400 秒）；set 后回读实际生效值放入 `config` 并列出 `changed`。其它参数（`dir` / `dbfilename` / `appendonly` / `save` / `requirepass` …）返回 `E_UNSUPPORTED` + 原因；服务商禁用 `CONFIG` 的实例返回 `E_UNSUPPORTED` 并说明替代做法。

## 配置契约（`redis.config.get` / `redis.config.set`）

遵循 `kore/docs/PLUGIN-CONFIG.md`：存储 `<KORE_STATE_DIR>/config.json`（0600 原子写），优先级 **config.json > 环境变量 > 代码默认值**，保存后同一进程内下一次调用立即生效（`restart_required=false`）。

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `host` | string | `127.0.0.1` | 主实例主机名/IP |
| `port` | int | `6379` | 主实例端口 |
| `password` | string（sensitive） | `""` | 主实例密码，只存本机 0600 文件；`config.get` 只回 `***` |
| `password_env` | string | `""` | 主实例密码所在环境变量名（不是密码值）；`password` 优先 |
| `instances` | list(string) | `[]` | 附加实例，每项 `<名称>=<主机>:<端口>` |
| `db` | int | `0` | 连接后 `SELECT` 的库号（0 为默认库，不再额外发 `SELECT`）；Redis 默认 0–15 |
| `timeout_s` | int | `5` | 建连与单次命令超时（1–30 秒） |
| `password_envs` | list(string) | `[]` | 附加实例密码环境变量，每项 `<名称>=<环境变量名>` |

示例：

```json
{
  "values": {
    "host": "127.0.0.1",
    "port": 6379,
    "db": 0,
    "timeout_s": 5,
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
- **命令白名单（三层防线，`internal/redisop/whitelist.go`）**：
  1. **方法面**：只注册上表 16 个业务方法，没有任意命令 / 脚本入口；
  2. **参数面**：新增方法用严格解码拒绝未知字段（夹带 `command` / `argv` / `script` 显式 `E_INVALID`）；`CONFIG SET` 只接受 `maxmemory` / `maxmemory-policy` / `timeout`，`dir` / `dbfilename` / `appendonly` / `save` / `requirepass` / `rename-command` 等返回 `E_UNSUPPORTED` + 原因；
  3. **传输面**：每条 RESP 命令发出前校验命令名与危险子命令，白名单外**本地拒绝**（不写入连接）。
- **明确拒绝**：`FLUSHALL` / `FLUSHDB` / `EVAL` / `EVALSHA` / `SCRIPT` / `FUNCTION` / `SHUTDOWN` / `KEYS` / `SAVE` / `DEBUG` / `MONITOR` / `ACL` / `MODULE` / `CLUSTER` / `MIGRATE` / `SWAPDB` / `SLAVEOF` / `REPLICAOF` 一律拒绝，错误信息说明「为什么」与「用什么替代」（如 `SAVE` → `redis.bgsave`、`KEYS` → `redis.keys` 的 SCAN）。危险方法名（`redis.flushall` / `redis.eval` / `redis.save` …）同样拒绝并附原因。
- **超时与限额**：建连与命令超时默认 5s（配置 `timeout_s` 可调 1–30s）；`pattern` ≤256 字节，批量删除 ≤100 个键，单键 ≤1024 字节，RESP bulk ≤8 MiB；`redis.set` 单值 ≤1 MiB、单次 ≤1000 元素且总载荷 ≤4 MiB；`redis.expire` TTL ≤10 年。

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

`tests/fault/` 对 kill -9、超时、磁盘满、cgroup 超限四类故障注入到执行面，断言 16 个业务方法返回结构化错误，且 `Error.Code` 与故障类别一一对应（`E_UPSTREAM` / `E_UPSTREAM` / `E_INTERNAL` / `E_UNSUPPORTED`），并带无故障 sanity 反证。

<!-- oss:begin -->
## 开源与贡献

[![CI](https://github.com/kore-pl/kore-plugin-redis/actions/workflows/ci.yml/badge.svg)](https://github.com/kore-pl/kore-plugin-redis/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)

本仓库是 **kore** 的 `Redis 管理插件`（id `redis`），独立仓库、独立版本线、独立 CI（少触发：push main / tag / PR / 手动）。内核与协议见 [kore](https://github.com/kore-pl/kore)。

- **许可**：MIT（inbound = outbound，不要求 CLA）；提交用 `git commit -s` 签 DCO。
- **贡献**：[CONTRIBUTING.md](CONTRIBUTING.md)（构建、门禁、发版、边界）。
- **安全**：[SECURITY.md](SECURITY.md)——漏洞不要开公开 issue。
- **行为准则**：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
- **兼容性**：`protocol: kore.plugin.v1` · `requires_kore: >=0.1.0 <0.2.0` · `lifecycle: lazy` · 深度等级 `production`。
- **验证方式**：本地 `make check` 优先（CI 只在 push main / tag / PR / 手动跑；发布前 `make dist` 连跑两次核对 sha 一致）。
- **发布产物**：`make dist` → `dist/redis-0.6.0.tar.gz` + `.sha256`（位级可复现，连跑两次 sha 相同）。
- **相关仓库**：[索引 kore-plugins](https://github.com/kore-pl/kore-plugins) · [协议文档](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-PROTOCOL.md) · [SDK 指南](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-SDK.md)

<!-- oss:end -->
