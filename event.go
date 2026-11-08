package billing

import "time"

// Event is a processed webhook event, kept so a redelivery is not processed twice.
type Event struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Data        string    `json:"data,omitempty"`
	ProcessedAt time.Time `json:"processed_at"`
}
