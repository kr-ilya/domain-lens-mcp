# domain-lens-mcp

[English](README.md) · [Русский](README_ru.md)

MCP-сервер, который отвечает агенту на один вопрос: **можно ли зарегистрировать
этот домен прямо сейчас?**

RDAP сообщает, есть ли в реестре объект регистрации. Это более узкий вопрос, чем
возможность регистрации, поэтому сервер опрашивает несколько источников,
сверяет их между собой и возвращает нормализованный ответ вместе с уверенностью
и основаниями:

```json
{
  "domain": "example.com",
  "normalized_domain": "example.com",
  "tld": "com",
  "availability": "unavailable",
  "status": "registered",
  "confidence": "high",
  "evidence": ["dns:delegated", "rdap:registration_exists"],
  "sources": [
    { "provider": "rdap", "registration_status": "registered", "source": "https://rdap.verisign.com/com/v1/domain/example.com", "latency_ms": 460 },
    { "provider": "dns", "registration_status": "registered", "source": "system-resolver", "latency_ms": 262 }
  ],
  "checked_at": "2026-09-20T21:16:18Z"
}
```

| Поле | Значения | Смысл |
| --- | --- | --- |
| `availability` | `available`, `unavailable`, `unknown` | Можно ли зарегистрировать. **`unknown` означает, что ни один источник не дал ответа, и читать его как «свободен» нельзя** |
| `status` | `available`, `registered`, `reserved`, `premium`, `blocked`, `pending_delete`, `redemption`, `rate_limited`, `unknown` | Детализация вердикта |
| `confidence` | `high`, `medium`, `low` | Насколько источники авторитетны и согласованы |
| `evidence` | `rdap:no_registration`, `whois:no_match`, `dns:delegated`, … | Что именно сказал каждый источник |
| `conflict` | `true` / отсутствует | Источники противоречат друг другу |

## Источники и правила их сверки

```
MCP tools → engine → RDAP / WHOIS / DNS → resolver → normalized result
               ↑
    cache + rate limit + singleflight + circuit breaker
```

1. **RDAP** — авторитетный источник. Эндпоинты берутся из
   [реестра IANA bootstrap](https://data.iana.org/rdap/dns.json) (1200+ TLD);
   снапшот вшит в бинарь и раз в сутки обновляется в рантайме, так что
   поддерживать хардкод-список TLD не требуется.
2. **WHOIS (TCP/43)** покрывает TLD без RDAP-сервиса (`.de`, `.io`, `.eu`,
   `.ru`, …). Сервер для конкретного TLD определяется запросом к
   `whois.iana.org`. Разбор свободного текста WHOIS эвристический, поэтому ответ
   «свободен», полученный только из WHOIS, никогда не получает `confidence: high`.
3. **DNS (запрос NS)** служит подтверждением. Делегирование доказывает, что
   домен занят; отсутствие делегирования не доказывает ничего и никогда не даёт
   `available`.

Если RDAP сообщает об отсутствии регистрации, а DNS или WHOIS считают домен
занятым, вердикт остаётся за RDAP, `confidence` понижается до `low`, а флаг
`conflict` выставляется в `true`.

## Инструменты

| Инструмент | Назначение |
| --- | --- |
| `check_domain` | Один домен. Принимает Unicode-имена и вставленные URL, нормализует их сам |
| `check_domains` | До 100 доменов за вызов. Предпочтительнее серии `check_domain`: общий кэш, дедупликация, ограничение частоты. Возвращает `summary` |
| `get_domain_info` | Регистратор, даты регистрации и истечения, EPP-статусы, nameservers, события |
| `suggest_domains` | Разворачивает ваши базовые идеи в кандидатов (матрица TLD, префиксы, суффиксы, дефисы, TLD-хаки), проверяет их все и возвращает свободные с ранжированием |

`suggest_domains` оставляет творческую часть агенту и берёт на себя механическое
расширение и проверку:

```json
{
  "names": ["datalens", "domainlens"],
  "tlds": ["com", "io", "dev"],
  "prefixes": ["get", "try"],
  "suffixes": ["hq", "app"],
  "hyphenate": true,
  "limit": 8
}
```

Порядок ранжирования: приоритет TLD из запроса → чистое базовое имя → без
дефиса → короче.

## Запуск

Сначала соберите образ:

```bash
docker build -t domain-lens-mcp .
```

### stdio

```bash
docker run -i --rm domain-lens-mcp
```

Контейнер говорит по MCP через stdin/stdout — именно этого большинство клиентов
ожидает от локального сервера.

### Streamable HTTP

```bash
docker compose up -d
```

Эндпоинт MCP — `http://localhost:8080/mcp`, health — `/healthz`.
Публикуемый порт меняется через `HTTP_PORT`.

## Подключение клиента

### Claude Code

```bash
claude mcp add domain-lens -- docker run -i --rm domain-lens-mcp
```

К запущенному HTTP-экземпляру:

```bash
claude mcp add --transport http domain-lens http://localhost:8080/mcp
```

Добавьте `--scope user`, чтобы сервер был доступен во всех проектах, или
`--scope project`, чтобы поделиться им с командой через `.mcp.json` в репозитории.

### Codex CLI

```bash
codex mcp add domain-lens -- docker run -i --rm domain-lens-mcp
```

Либо прописать напрямую в `~/.codex/config.toml`:

```toml
[mcp_servers.domain-lens]
command = "docker"
args = ["run", "-i", "--rm", "domain-lens-mcp"]
```

Для запущенного HTTP-экземпляра:

```toml
[mcp_servers.domain-lens]
url = "http://localhost:8080/mcp"
```

### Claude Desktop

Отредактируйте `claude_desktop_config.json` — на macOS в
`~/Library/Application Support/Claude/`, на Windows в `%APPDATA%\Claude\`:

```json
{
  "mcpServers": {
    "domain-lens": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "domain-lens-mcp"]
    }
  }
}
```

После этого перезапустите приложение.

### Cursor

`~/.cursor/mcp.json` для всех проектов или `.cursor/mcp.json` внутри одного:

```json
{
  "mcpServers": {
    "domain-lens": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "domain-lens-mcp"]
    }
  }
}
```

### VS Code

`.vscode/mcp.json` в рабочей папке (обратите внимание на ключ `servers`):

```json
{
  "servers": {
    "domain-lens": {
      "type": "stdio",
      "command": "docker",
      "args": ["run", "-i", "--rm", "domain-lens-mcp"]
    }
  }
}
```

Для запущенного HTTP-экземпляра — `{ "type": "http", "url": "http://localhost:8080/mcp" }`.

### Любой другой клиент

Укажите ему команду `docker run -i --rm domain-lens-mcp` для stdio или адрес
`http://localhost:8080/mcp` для Streamable HTTP. Никакие учётные данные и
API-ключи не нужны.

## Конфигурация

У каждого параметра есть флаг и переменная окружения; флаг приоритетнее.

| ENV | Флаг | По умолчанию | Описание |
| --- | --- | --- | --- |
| `TRANSPORT` | `-transport` | `stdio` | `stdio` или `http` |
| `HTTP_ADDR` | `-http-addr` | `:8080` | Адрес прослушивания в режиме `http` |
| `LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `REQUEST_TIMEOUT` | `-request-timeout` | `10s` | Таймаут одного запроса к источнику |
| `MAX_CONCURRENCY` | `-max-concurrency` | `8` | Одновременных запросов наружу |
| `RDAP_RPS` | `-rdap-rps` | `5` | Запросов в секунду на RDAP-хост |
| `RDAP_RETRIES` | `-rdap-retries` | `2` | Ретраев при троттлинге и сетевых ошибках |
| `WHOIS_ENABLED` | `-whois` | `true` | Использовать WHOIS там, где нет RDAP |
| `WHOIS_RPS` | `-whois-rps` | `1` | Запросов в секунду на WHOIS-хост |
| `DNS_ENABLED` | `-dns` | `true` | Использовать делегирование DNS как подтверждение |
| `DNS_TIMEOUT` | `-dns-timeout` | `3s` | Таймаут DNS-запроса |
| `CACHE_TTL_AVAILABLE` | `-cache-ttl-available` | `1m` | TTL кэша для свободных доменов |
| `CACHE_TTL_UNAVAILABLE` | `-cache-ttl-unavailable` | `5m` | TTL кэша для занятых |
| `CACHE_TTL_UNKNOWN` | `-cache-ttl-unknown` | `15s` | TTL кэша для `unknown` |

Логи всегда идут в **stderr**, потому что stdout занят stdio-транспортом.

## Защита источников от перегрузки

LLM-агент спокойно запрашивает двести вариантов за один ход, поэтому в сервер
встроены: TTL-кэш со временем жизни, зависящим от исхода, дедупликация
одновременных одинаковых запросов (`singleflight`), token bucket на каждый
upstream-хост, общий лимит параллелизма, экспоненциальный backoff с учётом
`Retry-After` и circuit breaker. Сработавший circuit breaker даёт `unknown`,
а не ложный `available`.

## Разработка

Go 1.26 или Docker.

```bash
go test -race ./...
go vet ./...
```
