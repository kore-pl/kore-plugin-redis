# kore-plugin-redis (plugin d'administration Redis)

## Ce que fait le plugin

Se connecte aux instances Redis existantes de l'utilisateur et fournit la découverte d'instances, les informations, la navigation dans les clés, la lecture/écriture et la gestion du TTL, la suppression, quatre vues d'exploitation en lecture seule (persistance, mémoire, slowlog, keyInfo) et des réglages de configuration serveur en liste blanche. Aucun serveur Redis ni bibliothèque cliente intégrés (G-05/G-17).

Méthodes : redis.instances, redis.info, redis.keys, redis.get, redis.set, redis.delete, redis.ttl, redis.expire, redis.persistence, redis.memory, redis.slowlog, redis.keyInfo (tableaux complets dans DETAIL.md).

## Points clés

- Le client est une implémentation RESP minimale d'environ 200 lignes dans internal/redisop (net.Dial plus codec CRLF) ; redis-cli n'est pas requis sur l'hôte.
- Connexions courtes : une connexion TCP par appel, fermée à la fin ; aucune connexion persistante, aucun polling d'arrière-plan et aucune fuite de goroutine.
- Protocole kore.plugin.v1, lifecycle=lazy, depth_level=production ; 16 méthodes métier et les deux méthodes config déclarent toutes des capacités par méthode (lecture seule system:read, écriture system:admin) ; stdout réservé aux trames, journaux sur stderr.
- Les identifiants sont injectés par l'environnement/la configuration et ne sont jamais écrits dans les journaux ou le README ; les cibles sont limitées par une liste blanche d'instances et les changements de configuration ne permettent que des clés autorisées.

## Installation et vérification

Installez `redis` depuis le marché de plugins ou importez dist/redis-0.6.0.tar.gz produit par make dist. Vérifiez avec make check (gofmt, go vet, go test -timeout 200s), y compris les suites boundary et fault.

---

简体中文原文见 [DETAIL.md](DETAIL.md)。
