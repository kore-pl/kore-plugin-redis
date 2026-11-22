# kore-plugin-redis

Redis 管理插件（**骨架**）：覆盖 Redis 实例 / 内存策略 / 键扫描的方法面。本仓只交付协议骨架与参数校验，**管理的是用户已有的 Redis 实例，不内置 Redis 服务、不连接任何 Redis、内核零依赖**（G-05 无中间件 / 提案 §2.1 批 2）。

## 定位与边界

- **管理用户已有 Redis**：实例由使用者自行部署与维护，本插件只暴露"查看实例 / 配置内存策略 / 扫描键"的方法面。
- **不内置 redis-server 二进制**：不捆绑、不下载、不启动任何 Redis 服务，符合内核薄壳与 G-05 无中间件约束。
- **内核零依赖**：只依赖 `kore/pkg/pluginapi`（`kore.plugin.v1` 协议 SDK），不引入任何 Redis 客户端、MQ 或运行时组件。
- **骨架口径**：方法只做参数校验 + 结构化占位返回，不发起任何网络 I/O、不读写用户数据。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`
- 能力声明：`system:admin`
- 方法：
  - `redis.instances` — 返回实例列表占位（同时接受裸 `instances`）
  - `redis.scan` — 键前缀扫描占位（同时接受裸 `scan`）
- 握手的 stdout 留给协议帧，日志一律写 stderr

## 内存策略枚举

`redis.instances` / `redis.scan` 的 `policy` 参数接受 Redis 官方 8 种策略，空值归一为 `noeviction`：

`noeviction` · `allkeys-lru` · `allkeys-lfu` · `allkeys-random` · `volatile-lru` · `volatile-lfu` · `volatile-random` · `volatile-ttl`

未列举的策略返回 `E_INVALID`。策略只是逻辑配置，骨架口径下不改变任何真实实例的运行状态。

## 键前缀校验（骨架口径）

`redis.scan` 的 `prefix` 参数：

- 必填；为空时回落到环境变量 `KORE_REDIS_DEFAULT_PREFIX`
- 拒绝 NUL、控制字符、超长（>256 字节）
- 拒绝通配符 `*?[]`（骨架只接受字面前缀，不把 glob 当前缀）
- `limit` 范围 `(0, 10000]`，缺省 100；`cursor` 不得为负

非法参数返回 `E_INVALID`；未知方法返回 `E_NOTFOUND`。

## 四类故障注入

`tests/fault/` 四个文件分别注入 kill -9、超时、磁盘满、cgroup 超限，断言方法级返回结构化错误，且 `Error.Code` 与故障类别一一对应：

| 故障 | 文件 | 期望错误码 |
| --- | --- | --- |
| kill -9 | `kill_test.go` | `E_UPSTREAM` |
| 超时 | `timeout_test.go` | `E_UPSTREAM` |
| 磁盘满 | `disk_test.go` | `E_INTERNAL` |
| cgroup 超限 | `cgroup_test.go` | `E_UNSUPPORTED` |

每个用例还带无故障 sanity 反证与错误体结构校验，避免"永远失败"式假通过。

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
tar -tzf dist/redis-0.1.0.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` + chmod 归一），重复构建 sha256 不变；`size_bytes` 由打包脚本迭代回填，与实际包体偏差 ≤10%。
