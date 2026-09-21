package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/engine"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/rdap"
)

// fixtureProvider answers from a fixed table, standing in for a registry.
type fixtureProvider struct {
	responses map[string]core.ProviderResult
}

func (f *fixtureProvider) Name() string { return rdap.ProviderName }

func (f *fixtureProvider) Supports(context.Context, domainname.Name) bool { return true }

func (f *fixtureProvider) Check(_ context.Context, name domainname.Name) (core.ProviderResult, error) {
	response, ok := f.responses[name.ASCII]
	if !ok {
		response = core.ProviderResult{
			RegistrationStatus: core.RegistrationNotRegistered,
			Status:             core.StatusAvailable,
		}
	}
	response.Provider = rdap.ProviderName
	return response, nil
}

// newTestSession wires a client to the server over the SDK's in-memory transport.
func newTestSession(t *testing.T, responses map[string]core.ProviderResult) *mcp.ClientSession {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(
		[]provider.Provider{&fixtureProvider{responses: responses}},
		nil,
		engine.Options{MaxConcurrency: 4, RequestTimeout: time.Second},
		logger,
	)
	server := New(eng, "test", logger)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.mcp.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callTool invokes a tool and decodes its structured output.
func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, args any, out *T) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) reported an error: %s", name, textOf(result))
	}
	if out != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured content: %v", err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode structured content: %v", err)
		}
	}
	return result
}

func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestToolsAreAdvertised(t *testing.T) {
	session := newTestSession(t, nil)

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	found := make(map[string]bool)
	for _, tool := range tools.Tools {
		found[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
	for _, name := range []string{"check_domain", "check_domains", "get_domain_info", "suggest_domains"} {
		if !found[name] {
			t.Errorf("tool %q is not advertised", name)
		}
	}
}

func TestCheckDomainTool(t *testing.T) {
	session := newTestSession(t, map[string]core.ProviderResult{
		"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
	})

	var out core.CheckResult
	result := callTool(t, session, "check_domain", CheckDomainInput{Domain: "TAKEN.com"}, &out)

	if out.Availability != core.AvailabilityUnavailable {
		t.Errorf("Availability = %q, want unavailable", out.Availability)
	}
	if out.NormalizedDomain != "taken.com" {
		t.Errorf("NormalizedDomain = %q", out.NormalizedDomain)
	}
	// Clients without structured-content support read the text mirror.
	if !strings.Contains(textOf(result), `"availability": "unavailable"`) {
		t.Errorf("text content should mirror the structured result: %s", textOf(result))
	}
}

func TestCheckDomainsTool(t *testing.T) {
	session := newTestSession(t, map[string]core.ProviderResult{
		"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
	})

	var out CheckDomainsOutput
	callTool(t, session, "check_domains", CheckDomainsInput{
		Domains: []string{"taken.com", "free.com", "not a domain"},
	}, &out)

	if out.Summary.Total != 3 || out.Summary.Available != 1 || out.Summary.Unavailable != 1 || out.Summary.Unknown != 1 {
		t.Errorf("Summary = %+v", out.Summary)
	}
	if len(out.Results) != 3 {
		t.Errorf("Results = %d, want 3", len(out.Results))
	}
}

func TestCheckDomainsOnlyAvailable(t *testing.T) {
	session := newTestSession(t, map[string]core.ProviderResult{
		"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
	})

	var out CheckDomainsOutput
	callTool(t, session, "check_domains", CheckDomainsInput{
		Domains:       []string{"taken.com", "free.com"},
		OnlyAvailable: true,
	}, &out)

	if len(out.Results) != 1 || out.Results[0].NormalizedDomain != "free.com" {
		t.Errorf("Results = %+v, want only free.com", out.Results)
	}
	// The summary still reports everything that was checked.
	if out.Summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2", out.Summary.Total)
	}
}

func TestCheckDomainsRejectsOversizedBatch(t *testing.T) {
	session := newTestSession(t, nil)

	domains := make([]string, maxBatchSize+1)
	for i := range domains {
		domains[i] = "example.com"
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "check_domains",
		Arguments: CheckDomainsInput{Domains: domains},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("an oversized batch should be rejected")
	}
}

func TestSuggestDomainsTool(t *testing.T) {
	session := newTestSession(t, map[string]core.ProviderResult{
		"datalens.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
	})

	var out SuggestDomainsOutput
	callTool(t, session, "suggest_domains", SuggestDomainsInput{
		Names:    []string{"datalens"},
		TLDs:     []string{"com", "io"},
		Prefixes: []string{"get"},
		Limit:    5,
	}, &out)

	if out.Generated != 4 || out.Checked != 4 {
		t.Errorf("Generated/Checked = %d/%d, want 4/4", out.Generated, out.Checked)
	}
	for _, result := range out.Results {
		if result.Availability != core.AvailabilityAvailable {
			t.Errorf("%s is %q, want only available results by default", result.NormalizedDomain, result.Availability)
		}
		if result.NormalizedDomain == "datalens.com" {
			t.Errorf("the taken candidate should have been filtered out")
		}
	}
}

func TestSuggestDomainsRequiresInput(t *testing.T) {
	session := newTestSession(t, nil)

	for _, args := range []SuggestDomainsInput{
		{TLDs: []string{"com"}},
		{Names: []string{"datalens"}},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "suggest_domains",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if !result.IsError {
			t.Errorf("suggest_domains(%+v) should be rejected", args)
		}
	}
}

func TestGetDomainInfoTool(t *testing.T) {
	registered := time.Date(2013, 5, 1, 0, 0, 0, 0, time.UTC)
	session := newTestSession(t, map[string]core.ProviderResult{
		"taken.com": {
			RegistrationStatus: core.RegistrationRegistered,
			Status:             core.StatusRegistered,
			Info: &core.DomainInfo{
				NormalizedDomain: "taken.com",
				Registered:       true,
				Registrar:        "Example Registrar",
				RegistrationDate: &registered,
				Nameservers:      []string{"ns1.example.net"},
			},
		},
	})

	var out core.DomainInfo
	callTool(t, session, "get_domain_info", GetDomainInfoInput{Domain: "taken.com"}, &out)

	if !out.Registered || out.Registrar != "Example Registrar" {
		t.Errorf("DomainInfo = %+v", out)
	}
	if len(out.Nameservers) != 1 {
		t.Errorf("Nameservers = %v", out.Nameservers)
	}
}
