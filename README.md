# domain-lens-mcp

[English](README.md) · [Русский](README_ru.md)

An MCP server that answers one question for an AI agent: **can this domain be
registered right now?**

RDAP tells you whether a registration object exists at the registry. That is a
narrower question than registrability, so the server queries several sources,
reconciles them, and returns a normalized answer that carries its own confidence
and the evidence behind it:

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

| Field | Values | Meaning |
| --- | --- | --- |
| `availability` | `available`, `unavailable`, `unknown` | Whether you can register it. **`unknown` means no source could answer — never read it as free** |
| `status` | `available`, `registered`, `reserved`, `premium`, `blocked`, `pending_delete`, `redemption`, `rate_limited`, `unknown` | The detail behind the verdict |
| `confidence` | `high`, `medium`, `low` | How authoritative and how consistent the sources were |
| `evidence` | `rdap:no_registration`, `whois:no_match`, `dns:delegated`, … | What each source actually said |
| `conflict` | `true` / absent | The sources disagreed with each other |

## Sources and how they are reconciled

```
MCP tools → engine → RDAP / WHOIS / DNS → resolver → normalized result
               ↑
    cache + rate limit + singleflight + circuit breaker
```

1. **RDAP** is the authoritative source. Endpoints come from the
   [IANA bootstrap registry](https://data.iana.org/rdap/dns.json) (1200+ TLDs);
   a snapshot is embedded in the binary and refreshed at runtime once a day, so
   there is no hardcoded TLD table to maintain.
2. **WHOIS (TCP/43)** covers TLDs with no RDAP service (`.de`, `.io`, `.eu`,
   `.ru`, …). The server for a TLD is discovered by querying `whois.iana.org`.
   Reading a free-form WHOIS reply is heuristic, so an *available* answer from
   WHOIS alone never reaches `high` confidence.
3. **DNS (NS lookup)** is corroboration. Delegation proves a domain is taken;
   the absence of delegation proves nothing and never produces `available`.

When RDAP reports no registration while DNS or WHOIS says the domain is taken,
the RDAP verdict stands, `confidence` drops to `low`, and `conflict` is set.

## Tools

| Tool | Purpose |
| --- | --- |
| `check_domain` | One domain. Accepts Unicode names and pasted URLs, and normalizes them |
| `check_domains` | Up to 100 domains per call. Preferred over a series of `check_domain` calls: shared cache, deduplication, rate limiting. Returns a `summary` |
| `get_domain_info` | Registrar, registration and expiration dates, EPP statuses, nameservers, events |
| `suggest_domains` | Expands your base name ideas into candidates (TLD matrix, prefixes, suffixes, hyphens, TLD hacks), checks them all, returns the available ones ranked |

`suggest_domains` leaves the creative part to the agent and does the mechanical
expansion and verification:

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

Ranking order: requested TLD priority → plain base name → no hyphen → shorter.

## Running

Build the image first:

```bash
docker build -t domain-lens-mcp .
```

### stdio

```bash
docker run -i --rm domain-lens-mcp
```

The container speaks MCP over stdin/stdout, which is what most clients expect
from a local server.

### Streamable HTTP

```bash
docker compose up -d
```

The MCP endpoint is `http://localhost:8080/mcp`, health is at `/healthz`.
Change the published port with `HTTP_PORT`.

## Connecting a client

### Claude Code

```bash
claude mcp add domain-lens -- docker run -i --rm domain-lens-mcp
```

Against a running HTTP instance:

```bash
claude mcp add --transport http domain-lens http://localhost:8080/mcp
```

Add `--scope user` to make it available in every project, or `--scope project`
to share it with your team through a checked-in `.mcp.json`.

### Codex CLI

```bash
codex mcp add domain-lens -- docker run -i --rm domain-lens-mcp
```

Or write it into `~/.codex/config.toml` directly:

```toml
[mcp_servers.domain-lens]
command = "docker"
args = ["run", "-i", "--rm", "domain-lens-mcp"]
```

A running HTTP instance instead:

```toml
[mcp_servers.domain-lens]
url = "http://localhost:8080/mcp"
```

### Claude Desktop

Edit `claude_desktop_config.json` — on macOS at
`~/Library/Application Support/Claude/`, on Windows at `%APPDATA%\Claude\`:

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

Restart the app afterwards.

### Cursor

`~/.cursor/mcp.json` for every project, or `.cursor/mcp.json` inside one:

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

`.vscode/mcp.json` in the workspace (note the `servers` key):

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

For a running HTTP instance, use `{ "type": "http", "url": "http://localhost:8080/mcp" }`.

### Any other client

Point it at the `docker run -i --rm domain-lens-mcp` command for stdio, or at
`http://localhost:8080/mcp` for Streamable HTTP. No credentials or API keys are
involved.

## Configuration

Every setting has a flag and an environment variable; the flag wins.

| ENV | Flag | Default | Description |
| --- | --- | --- | --- |
| `TRANSPORT` | `-transport` | `stdio` | `stdio` or `http` |
| `HTTP_ADDR` | `-http-addr` | `:8080` | Listen address in `http` transport |
| `LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `REQUEST_TIMEOUT` | `-request-timeout` | `10s` | Timeout for a single upstream query |
| `MAX_CONCURRENCY` | `-max-concurrency` | `8` | Simultaneous upstream queries |
| `RDAP_RPS` | `-rdap-rps` | `5` | Requests per second per RDAP host |
| `RDAP_RETRIES` | `-rdap-retries` | `2` | Retries on throttling and transport errors |
| `WHOIS_ENABLED` | `-whois` | `true` | Use WHOIS where RDAP is unavailable |
| `WHOIS_RPS` | `-whois-rps` | `1` | Requests per second per WHOIS host |
| `DNS_ENABLED` | `-dns` | `true` | Use DNS delegation as corroborating evidence |
| `DNS_TIMEOUT` | `-dns-timeout` | `3s` | Timeout for a DNS lookup |
| `CACHE_TTL_AVAILABLE` | `-cache-ttl-available` | `1m` | Cache lifetime for available results |
| `CACHE_TTL_UNAVAILABLE` | `-cache-ttl-unavailable` | `5m` | Cache lifetime for unavailable results |
| `CACHE_TTL_UNKNOWN` | `-cache-ttl-unknown` | `15s` | Cache lifetime for `unknown` results |

Logs always go to **stderr**, because stdout carries the stdio transport.

## Protecting upstream registries

An LLM agent will happily ask about two hundred candidates in a single turn, so
the server ships with a TTL cache whose lifetime depends on the outcome,
deduplication of concurrent identical lookups (`singleflight`), a token bucket
per upstream host, a global concurrency limit, exponential backoff that honours
`Retry-After`, and a circuit breaker. An open circuit yields `unknown` rather
than a false `available`.

## Development

Go 1.26, or Docker.

```bash
go test -race ./...
go vet ./...
```
