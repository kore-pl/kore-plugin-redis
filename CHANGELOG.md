# Changelog

## 0.1.0 (2026-11-22)

- feat: 新建 redis 管理插件骨架，复用 kore.plugin.v1 握手与会话循环
- feat: redis.instances / redis.scan 方法级参数校验与结构化结果——内存策略枚举（allkeys-lru/noeviction 等 8 种）+ key 前缀字面前缀校验，管理用户已有 Redis，不内置 redis-server、内核零依赖
- feat: 四类故障注入测试（kill -9 / 超时 / 磁盘满 / cgroup 超限），验证方法级错误返回与无故障反证
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner + gzip -9n + chmod 归一）
