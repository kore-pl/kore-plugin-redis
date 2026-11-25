# kore-plugin-redis (plugin de gestión de Redis)

## Qué hace

Se conecta a instancias Redis que el usuario ya tiene y ofrece detección de instancias, información, exploración de claves, lectura/escritura y TTL, borrado, cuatro vistas operativas de solo lectura (persistencia, memoria, slowlog, keyInfo) y ajustes de configuración del servidor en lista blanca. No incluye servidor Redis ni biblioteca cliente (G-05/G-17).

Métodos: redis.instances, redis.info, redis.keys, redis.get, redis.set, redis.delete, redis.ttl, redis.expire, redis.persistence, redis.memory, redis.slowlog, redis.keyInfo (tablas completas en DETAIL.md).

## Puntos clave

- El cliente es una implementación RESP mínima de ~200 líneas en internal/redisop (net.Dial más codec CRLF); no hace falta redis-cli en el host.
- Conexiones cortas: una conexión TCP por llamada, cerrada al terminar; sin conexiones persistentes, sondeo en segundo plano ni fugas de gorutinas.
- Protocolo kore.plugin.v1, lifecycle=lazy, depth_level=production; 16 métodos de negocio más los dos de config declaran capacidades por método (solo lectura system:read, escritura system:admin); stdout reservado a tramas, registros a stderr.
- Las credenciales se inyectan por entorno/configuración y nunca se escriben en registros ni README; los destinos están limitados por una lista blanca de instancias y los cambios de configuración solo permiten claves autorizadas.

## Instalación y verificación

Instale `redis` desde el mercado de plugins o importe dist/redis-0.6.0.tar.gz generado con make dist. Verifique con make check (gofmt, go vet, go test -timeout 200s), incluidas las suites boundary y fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
