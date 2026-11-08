package billing

import (
	"context"
	"fmt"
)

// ReportUsage sends value units of a meter's event for the app's customer. identifier makes a retried report count once.
func (s *Service) ReportUsage(ctx context.Context, customerEmail, meter string, value int64, identifier string) error {
	cust, err := s.customers.FindByEmailAndApp(ctx, customerEmail, s.appID)
	if err != nil {
		return fmt.Errorf("find customer: %w", err)
	}
	if err := s.provider.ReportUsage(ctx, meter, cust.ProviderCustomerID, value, identifier); err != nil {
		return fmt.Errorf("report usage: %w", err)
	}
	return nil
}
