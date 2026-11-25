# kore-plugin-redis (Plugin für Redis-Verwaltung)

## Was es tut

Verbindet sich mit vorhandenen Redis-Instanzen des Benutzers und bietet Instanzerkennung, Informationen, Schlüssel browsing, Lesen/Schreiben und TTL-Verwaltung, Löschen sowie vier schreibfreie Betriebsansichten (Persistenz, Speicher, Slowlog, keyInfo) und Allowlist-basierte Server-Konfigurationsänderungen. Kein Redis-Server und keine Client-Bibliothek integriert (G-05/G-17).

Methoden: redis.instances, redis.info, redis.keys, redis.get, redis.set, redis.delete, redis.ttl, redis.expire, redis.persistence, redis.memory, redis.slowlog, redis.keyInfo (vollständige Tabellen in DETAIL.md).

## Wichtige Punkte

- Der Client ist eine ~200 Zeilen kleine RESP-Implementierung in internal/redisop (net.Dial plus CRLF-Codec); redis-cli auf dem Host ist nicht nötig.
- Kurze Verbindungen: eine TCP-Verbindung pro Methodenaufruf, danach geschlossen; keine dauerhaften Verbindungen, kein Hintergrund-Polling und keine Goroutine-Leaks.
- Protokoll kore.plugin.v1, lifecycle=lazy, depth_level=production; 16 Fachmethoden plus die zwei config-Methoden deklarieren alle Methoden-Fähigkeiten (schreibfrei system:read, schreibend system:admin); stdout für Protokollrahmen reserviert, Logs nach stderr.
- Zugangsdaten werden über Umgebung/Konfiguration injiziert und erscheinen nie in Logs oder README; Verbindungsziele sind durch eine Instanz-Allowlist begrenzt, Konfigurationsänderungen erlauben nur freigegebene Schlüssel.

## Installation und Prüfung

Installieren Sie `redis` über den Plugin-Markt oder importieren Sie das mit make dist erzeugte dist/redis-0.6.0.tar.gz. Prüfen Sie mit make check (gofmt, go vet, go test -timeout 200s), einschließlich boundary- und fault-Tests.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
