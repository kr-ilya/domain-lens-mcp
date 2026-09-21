// Package core defines the normalized domain availability vocabulary shared by
// providers, the resolution engine and the MCP layer.
package core

import "time"

// Availability is the actionable answer to "can I register this domain right now?".
type Availability string

const (
	// AvailabilityAvailable means no registration exists and registration looks possible.
	AvailabilityAvailable Availability = "available"
	// AvailabilityUnavailable means the domain cannot be registered as-is right now.
	AvailabilityUnavailable Availability = "unavailable"
	// AvailabilityUnknown means no source produced a trustworthy answer. It must
	// never be treated as "available".
	AvailabilityUnknown Availability = "unknown"
)

// Status carries the detail behind an Availability, because registries expose
// many shades of "taken" that matter when picking a name.
type Status string

const (
	StatusAvailable     Status = "available"
	StatusRegistered    Status = "registered"
	StatusReserved      Status = "reserved"
	StatusPremium       Status = "premium"
	StatusBlocked       Status = "blocked"
	StatusPendingDelete Status = "pending_delete"
	StatusRedemption    Status = "redemption"
	StatusRateLimited   Status = "rate_limited"
	StatusUnknown       Status = "unknown"
)

// Confidence reflects how much agreement and authority backs the Availability.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// RegistrationStatus is what a single provider can honestly report: whether a
// registration object exists, not whether the domain is registrable.
type RegistrationStatus string

const (
	RegistrationRegistered    RegistrationStatus = "registered"
	RegistrationNotRegistered RegistrationStatus = "not_registered"
	RegistrationUnknown       RegistrationStatus = "unknown"
)

// ProviderResult is the outcome of querying one source for one domain.
type ProviderResult struct {
	Provider           string             `json:"provider"`
	RegistrationStatus RegistrationStatus `json:"registration_status"`
	Status             Status             `json:"status"`
	// Evidence are stable machine-readable tags such as "rdap:no_registration".
	Evidence  []string `json:"evidence,omitempty"`
	Source    string   `json:"source,omitempty"`
	LatencyMS int64    `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
	// Raw is the untouched provider payload, returned only on request.
	Raw any `json:"raw,omitempty"`
	// Info holds registration details when the provider could parse them.
	Info *DomainInfo `json:"-"`
}

// CheckResult is the normalized answer returned by the availability engine.
type CheckResult struct {
	Domain           string           `json:"domain"`
	NormalizedDomain string           `json:"normalized_domain"`
	TLD              string           `json:"tld"`
	Availability     Availability     `json:"availability"`
	Status           Status           `json:"status"`
	Confidence       Confidence       `json:"confidence"`
	Evidence         []string         `json:"evidence"`
	Conflict         bool             `json:"conflict,omitempty"`
	Sources          []ProviderResult `json:"sources,omitempty"`
	CheckedAt        time.Time        `json:"checked_at"`
	Cached           bool             `json:"cached,omitempty"`
	Error            string           `json:"error,omitempty"`
}

// DomainInfo carries registration metadata, independent of availability.
type DomainInfo struct {
	Domain           string     `json:"domain"`
	NormalizedDomain string     `json:"normalized_domain"`
	Registered       bool       `json:"registered"`
	Registrar        string     `json:"registrar,omitempty"`
	RegistrarIANAID  string     `json:"registrar_iana_id,omitempty"`
	RegistrationDate *time.Time `json:"registration_date,omitempty"`
	ExpirationDate   *time.Time `json:"expiration_date,omitempty"`
	LastChangedDate  *time.Time `json:"last_changed_date,omitempty"`
	Statuses         []string   `json:"statuses,omitempty"`
	Nameservers      []string   `json:"nameservers,omitempty"`
	DNSSEC           *bool      `json:"dnssec,omitempty"`
	Events           []Event    `json:"events,omitempty"`
	Source           string     `json:"source,omitempty"`
	Provider         string     `json:"provider,omitempty"`
	Raw              any        `json:"raw,omitempty"`
}

// Event is an RDAP-style lifecycle event (registration, expiration, transfer...).
type Event struct {
	Action string    `json:"action"`
	Date   time.Time `json:"date"`
	Actor  string    `json:"actor,omitempty"`
}
