package api

// DnsCredentialsResponse is the response from DNS credentials endpoints
type DnsCredentialsResponse struct {
	Message   string              `json:"message,omitempty"`
	Provider  string              `json:"provider,omitempty"`
	Providers []DnsProviderStatus `json:"providers,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// DnsProviderStatus shows a configured DNS provider
type DnsProviderStatus struct {
	Provider     string `json:"provider"`
	ConfiguredAt string `json:"configured_at"`
}

// DomainCheckResponse is the response from domain availability check
type DomainCheckResponse struct {
	Domain    string           `json:"domain"`
	Available bool             `json:"available"`
	Premium   bool             `json:"premium"`
	Price     *DomainPriceInfo `json:"price,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// DomainPriceInfo contains pricing info for domain registration
type DomainPriceInfo struct {
	RegistrationCents int    `json:"registration_cents"`
	RenewalCents      int    `json:"renewal_cents"`
	Currency          string `json:"currency"`
}

// DomainRegisterResponse is the response from domain registration
type DomainRegisterResponse struct {
	Domain            string `json:"domain"`
	RequestedHostname string `json:"requested_hostname"`
	RegistrationID    string `json:"registration_id"`
	PriceCents        int    `json:"price_cents"`
	CheckoutURL       string `json:"checkout_url,omitempty"`
	Status            string `json:"status"`
	LastError         string `json:"last_error,omitempty"`
	Error             string `json:"error,omitempty"`
}
