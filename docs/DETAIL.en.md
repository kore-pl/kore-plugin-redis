# kore-plugin-redis (Redis management plugin)

## What it does

Connects to Redis instances the user already has, providing instance discovery, info, key browsing, get/set/TTL management, deletion and four read-only operational views (persistence, memory, slowlog, keyInfo) plus allowlisted server-config changes. It does not bundle a Redis server or any Redis client library (G-05/G-17).

Methods: redis.instances, redis.info, redis.keys, redis.get, redis.set, redis.delete, redis.ttl, redis.expire, redis.persistence, redis.memory, redis.slowlog, redis.keyInfo (full tables in DETAIL.md).

## Key points

- The client is a ~200-line minimal RESP implementation in internal/redisop (net.Dial plus CRLF codec); redis-cli on the host is not required.
- Short connections: one TCP connection per method call, closed when the call ends; no persistent connections, no background polling and no goroutine leaks.
- Protocol kore.plugin.v1, lifecycle=lazy, depth_level=production; 16 business methods plus the two config methods all declare method-level capabilities (read-only system:read, writes system:admin); stdout reserved for protocol frames, logs go to stderr.
- Credentials come from environment/config injection and are never written to logs or README; connection targets are constrained by an instance allowlist, and server-config changes only allow allowlisted keys.

## Installation and verification

Install `redis` from the plugin marketplace or import dist/redis-0.6.0.tar.gz built with make dist. Verify with make check (gofmt, go vet, go test -timeout 200s), including boundary and fault suites.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
