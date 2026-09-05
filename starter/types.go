package main

// Event is one webhook event from a message provider. This is the fixed
// wire contract — do not add required fields beyond what providers send.
type Event struct {
	EventID    string            `json:"event_id"`
	CampaignID string            `json:"campaign_id"`
	ContactID  string            `json:"contact_id"`
	Type       string            `json:"type"` // sent | delivered | opened | clicked
	Timestamp  string            `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ItemStatus is the outcome of processing a single event within a batch.
type ItemStatus string

const (
	StatusAccepted  ItemStatus = "accepted"
	StatusDuplicate ItemStatus = "duplicate"
	StatusConflict  ItemStatus = "conflict"
	StatusRejected  ItemStatus = "rejected"
)

// ItemResult reports what happened to one element of a POST /events batch.
type ItemResult struct {
	Index   int        `json:"index"`
	EventID string     `json:"event_id,omitempty"`
	Status  ItemStatus `json:"status"`
	Reason  string     `json:"reason,omitempty"`
}

// BatchResponse is the body of a successful POST /events response.
type BatchResponse struct {
	Accepted  int          `json:"accepted"`
	Duplicate int          `json:"duplicate"`
	Conflict  int          `json:"conflict"`
	Rejected  int          `json:"rejected"`
	Results   []ItemResult `json:"results"`
}

// StatsResponse is the body of a GET /campaigns/{id}/stats response.
type StatsResponse struct {
	CampaignID  string `json:"campaign_id"`
	Sent        int    `json:"sent"`
	Delivered   int    `json:"delivered"`
	Opened      int    `json:"opened"`
	UniqueOpens int    `json:"unique_opens"`
	Clicked     int    `json:"clicked"`
}
