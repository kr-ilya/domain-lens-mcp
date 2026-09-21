// Command domain-lens-mcp serves domain availability tools over MCP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/engine"
	"github.com/kr-ilya/domain-lens-mcp/internal/mcpserver"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/dnsprobe"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/rdap"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/whois"
	"github.com/kr-ilya/domain-lens-mcp/internal/ratelimit"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// cachePurgeInterval bounds how long expired entries linger in memory.
const cachePurgeInterval = 5 * time.Minute

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "domain-lens-mcp: %v\n", err)
		os.Exit(1)
	}
}

// config holds every runtime knob, resolved from flags then environment.
type config struct {
	transport      string
	httpAddr       string
	logLevel       string
	requestTimeout time.Duration
	maxConcurrency int
	rdapRPS        float64
	rdapRetries    int
	whoisEnabled   bool
	whoisRPS       float64
	dnsEnabled     bool
	dnsTimeout     time.Duration
	cacheTTL       engine.CacheTTL
	// healthcheck probes a running http-transport server and exits, so the
	// distroless image can health-check itself without a shell or curl.
	healthcheck bool
}

func run() error {
	cfg := loadConfig()
	if cfg.healthcheck {
		return probeHealth(cfg.httpAddr)
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	// stdout carries the stdio transport, so every log line goes to stderr.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(cfg.logLevel)}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eng := buildEngine(cfg, logger)

	go purgeCachePeriodically(ctx, eng)

	server := mcpserver.New(eng, version, logger)
	logger.Info("starting domain-lens-mcp", "version", version, "transport", cfg.transport)

	switch cfg.transport {
	case "http":
		return server.RunHTTP(ctx, cfg.httpAddr)
	default:
		return server.RunStdio(ctx)
	}
}

func buildEngine(cfg config, logger *slog.Logger) *engine.Engine {
	httpClient := &http.Client{
		Timeout: cfg.requestTimeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
	}

	rdapLimiter := ratelimit.NewHostLimiter(ratelimit.Config{RPS: cfg.rdapRPS, Burst: int(cfg.rdapRPS * 2)})
	bootstrap := rdap.NewBootstrap(httpClient, 24*time.Hour, logger)

	primary := []provider.Provider{
		rdap.New(bootstrap, httpClient, rdapLimiter, cfg.rdapRetries, logger),
	}
	if cfg.whoisEnabled {
		whoisLimiter := ratelimit.NewHostLimiter(ratelimit.Config{RPS: cfg.whoisRPS, Burst: 2})
		primary = append(primary, whois.New(whoisLimiter, cfg.requestTimeout, logger))
	}

	var corroborating []provider.Provider
	if cfg.dnsEnabled {
		corroborating = append(corroborating, dnsprobe.New(cfg.dnsTimeout))
	}

	return engine.New(primary, corroborating, engine.Options{
		MaxConcurrency: cfg.maxConcurrency,
		RequestTimeout: cfg.requestTimeout,
		CacheTTL:       cfg.cacheTTL,
	}, logger)
}

func purgeCachePeriodically(ctx context.Context, eng *engine.Engine) {
	ticker := time.NewTicker(cachePurgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			eng.PurgeCache()
		}
	}
}

func loadConfig() config {
	cfg := config{}
	flag.StringVar(&cfg.transport, "transport", envString("TRANSPORT", "stdio"), "transport: stdio or http")
	flag.StringVar(&cfg.httpAddr, "http-addr", envString("HTTP_ADDR", ":8080"), "listen address in http transport")
	flag.BoolVar(&cfg.healthcheck, "healthcheck", false, "probe a running http server on HTTP_ADDR and exit")
	flag.StringVar(&cfg.logLevel, "log-level", envString("LOG_LEVEL", "info"), "log level: debug, info, warn, error")
	flag.DurationVar(&cfg.requestTimeout, "request-timeout", envDuration("REQUEST_TIMEOUT", 10*time.Second), "timeout for a single upstream query")
	flag.IntVar(&cfg.maxConcurrency, "max-concurrency", envInt("MAX_CONCURRENCY", 8), "maximum simultaneous upstream queries")
	flag.Float64Var(&cfg.rdapRPS, "rdap-rps", envFloat("RDAP_RPS", 5), "requests per second per RDAP host")
	flag.IntVar(&cfg.rdapRetries, "rdap-retries", envInt("RDAP_RETRIES", 2), "retries per RDAP request on throttling or transport errors")
	flag.BoolVar(&cfg.whoisEnabled, "whois", envBool("WHOIS_ENABLED", true), "use WHOIS where RDAP is unavailable")
	flag.Float64Var(&cfg.whoisRPS, "whois-rps", envFloat("WHOIS_RPS", 1), "requests per second per WHOIS host")
	flag.BoolVar(&cfg.dnsEnabled, "dns", envBool("DNS_ENABLED", true), "use DNS delegation as corroborating evidence")
	flag.DurationVar(&cfg.dnsTimeout, "dns-timeout", envDuration("DNS_TIMEOUT", 3*time.Second), "timeout for a DNS lookup")
	flag.DurationVar(&cfg.cacheTTL.Available, "cache-ttl-available", envDuration("CACHE_TTL_AVAILABLE", time.Minute), "cache lifetime for available results")
	flag.DurationVar(&cfg.cacheTTL.Unavailable, "cache-ttl-unavailable", envDuration("CACHE_TTL_UNAVAILABLE", 5*time.Minute), "cache lifetime for unavailable results")
	flag.DurationVar(&cfg.cacheTTL.Unknown, "cache-ttl-unknown", envDuration("CACHE_TTL_UNKNOWN", 15*time.Second), "cache lifetime for unknown results")
	flag.Parse()
	return cfg
}

// probeHealth requests the health endpoint of a locally running server.
func probeHealth(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid http address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned status %d", resp.StatusCode)
	}
	return nil
}

func (c config) validate() error {
	if c.transport != "stdio" && c.transport != "http" {
		return fmt.Errorf("unsupported transport %q, expected stdio or http", c.transport)
	}
	if c.maxConcurrency <= 0 {
		return fmt.Errorf("max-concurrency must be positive, got %d", c.maxConcurrency)
	}
	if c.rdapRPS <= 0 || c.whoisRPS <= 0 {
		return fmt.Errorf("rate limits must be positive")
	}
	return nil
}

func parseLevel(value string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return slog.LevelInfo
	}
	return level
}

func envString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envFloat(key string, fallback float64) float64 {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
