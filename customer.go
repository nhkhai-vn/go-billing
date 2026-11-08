package billing

import "time"

// Customer is one email's billing identity in one app; the same email in two apps is two customers.
type Customer struct {
	ID                 string    `json:"id"`
	Email              string    `json:"email"`
	AppID              string    `json:"app_id"`
	Name               string    `json:"name"`
	ProviderCustomerID string    `json:"provider_customer_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func customerID(appID, email string) string { return appID + "|" + email }
