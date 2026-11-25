# kore-plugin-redis (plugin de gerenciamento do Redis)

## O que faz

Conecta-se a instâncias Redis já existentes do usuário e oferece descoberta de instâncias, informações, navegação de chaves, leitura/escrita e TTL, exclusão, quatro visões operacionais somente leitura (persistência, memória, slowlog, keyInfo) e ajustes de configuração do servidor em lista branca. Não inclui servidor Redis nem biblioteca cliente (G-05/G-17).

Métodos: redis.instances, redis.info, redis.keys, redis.get, redis.set, redis.delete, redis.ttl, redis.expire, redis.persistence, redis.memory, redis.slowlog, redis.keyInfo (tabelas completas em DETAIL.md).

## Pontos-chave

- O cliente é uma implementação RESP mínima de ~200 linhas em internal/redisop (net.Dial mais codec CRLF); redis-cli no host não é necessário.
- Conexões curtas: uma conexão TCP por chamada, fechada ao final; sem conexões persistentes, polling em segundo plano ou vazamento de goroutines.
- Protocolo kore.plugin.v1, lifecycle=lazy, depth_level=production; 16 métodos de negócio e os dois de config declaram capacidades por método (leitura system:read, escrita system:admin); stdout reservado para quadros do protocolo, logs em stderr.
- Credenciais vêm de ambiente/configuração e nunca aparecem em logs ou README; destinos são limitados por lista branca de instâncias e mudanças de configuração só permitem chaves autorizadas.

## Instalação e verificação

Instale `redis` no mercado de plugins ou importe dist/redis-0.6.0.tar.gz gerado por make dist. Verifique com make check (gofmt, go vet, go test -timeout 200s), incluindo as suítes boundary e fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
