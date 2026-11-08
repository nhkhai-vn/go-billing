package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

func (s *Service) CreateCheckout(ctx context.Context, in CheckoutInput) (checkoutURL, sessionID string, err error) {
	priceID, err := s.resolvePriceID(ctx, in.PriceID, in.ProductID)
	if err != nil {
		return "", "", err
	}

	cust, err := s.findOrCreateCustomer(ctx, in.CustomerEmail, in.CustomerName)
	if err != nil {
		return "", "", fmt.Errorf("find or create customer: %w", err)
	}

	trialDays, setupPriceID := s.resolveFirstTimePerks(ctx, cust.ID, in.TrialDays, in.SetupPriceID)

	sess, err := s.provider.CreateCheckout(ctx, CheckoutParams{
		CustomerID:          cust.ProviderCustomerID,
		PriceID:             priceID,
		Quantity:            units(in.Quantity),
		SetupPriceID:        setupPriceID,
		MeteredPriceID:      in.MeteredPriceID,
		SuccessURL:          in.SuccessURL,
		CancelURL:           in.CancelURL,
		AppID:               s.appID,
		CustomerEmail:       in.CustomerEmail,
		TrialDays:           trialDays,
		AllowPromotionCodes: in.AllowPromotionCodes,
		Locale:              in.Locale,
	})
	if err != nil {
		return "", "", fmt.Errorf("create checkout session: %w", err)
	}

	slog.InfoContext(ctx, "checkout session created", "app_id", s.appID, "email", in.CustomerEmail, "price_id", priceID, "product_id", in.ProductID, "quantity", units(in.Quantity), "session_id", sess.ID)
	return sess.URL, sess.ID, nil
}

func units(quantity int64) int64 { return max(quantity, 1) }

func (s *Service) resolvePriceID(ctx context.Context, priceID, productID string) (string, error) {
	if priceID != "" {
		return priceID, nil
	}
	p, err := s.provider.GetProduct(ctx, productID)
	if errors.Is(err, ErrNotFound) {
		return "", ErrNoDefaultPrice
	}
	if err != nil {
		return "", fmt.Errorf("get product: %w", err)
	}
	if !p.Active || p.DefaultPriceID == "" {
		return "", ErrNoDefaultPrice
	}
	return p.DefaultPriceID, nil
}

// resolveFirstTimePerks withholds the trial and setup fee unless this is the customer's first subscription to the app.
// A never-paid incomplete attempt does not count, so a failed first checkout keeps the perks. When the history cannot
// be read it fails closed, withholding both rather than granting a perk it cannot justify.
func (s *Service) resolveFirstTimePerks(ctx context.Context, customerID string, trialDays int, setupPriceID string) (int, string) {
	if trialDays == 0 && setupPriceID == "" {
		return trialDays, setupPriceID
	}

	sub, err := s.subs.FindByCustomerAndApp(ctx, customerID, s.appID)
	if errors.Is(err, ErrNotFound) {
		return trialDays, setupPriceID
	}
	if err != nil {
		slog.WarnContext(ctx, "could not check subscription history; withholding first-time perks", "error", err)
		return 0, ""
	}
	if sub.Status == StatusIncomplete || sub.Status == StatusIncompleteExpired {
		return trialDays, setupPriceID
	}
	return 0, ""
}

// ConfirmCheckout settles a checkout when the customer returns from the provider, without waiting for the webhook.
func (s *Service) ConfirmCheckout(ctx context.Context, sessionID string) (*Subscription, error) {
	sess, err := s.provider.GetCheckout(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get checkout session: %w", err)
	}
	if sess.AppID != s.appID {
		return nil, ErrAppMismatch
	}
	if !sess.Paid {
		return nil, ErrPaymentPending
	}
	if sess.Subscription == nil {
		return nil, errors.New("session has no subscription")
	}

	cust, err := s.customers.FindByEmailAndApp(ctx, sess.CustomerEmail, s.appID)
	if err != nil {
		return nil, fmt.Errorf("find customer: %w", err)
	}

	sub, err := s.upsertSubscription(ctx, cust.ID, sess.Subscription)
	if err != nil {
		return nil, fmt.Errorf("upsert subscription: %w", err)
	}

	slog.InfoContext(ctx, "checkout confirmed", "app_id", s.appID, "email", sess.CustomerEmail, "subscription_id", sub.ProviderSubscriptionID)
	return sub, nil
}

// GetSubscriptionStatus returns the customer's subscription, or nil when they have none.
func (s *Service) GetSubscriptionStatus(ctx context.Context, customerEmail string) (*Subscription, error) {
	cust, err := s.customers.FindByEmailAndApp(ctx, customerEmail, s.appID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find customer: %w", err)
	}

	sub, err := s.subs.FindByCustomerAndApp(ctx, cust.ID, s.appID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find subscription: %w", err)
	}
	return sub, nil
}

// ListSubscriptions returns every subscription of the app; callers filter by status.
func (s *Service) ListSubscriptions(ctx context.Context) ([]*Subscription, error) {
	subs, err := s.subs.ListByApp(ctx, s.appID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions by app: %w", err)
	}
	return subs, nil
}

func (s *Service) customerSubscription(ctx context.Context, customerEmail string) (*Customer, *Subscription, error) {
	cust, err := s.customers.FindByEmailAndApp(ctx, customerEmail, s.appID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrCustomerNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("find customer: %w", err)
	}
	sub, err := s.subs.FindByCustomerAndApp(ctx, cust.ID, s.appID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrSubNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("find subscription: %w", err)
	}
	return cust, sub, nil
}

// CancelSubscription cancels now or at the period end. It returns ErrCustomerNotFound or ErrSubNotFound when there is
// nothing to cancel.
func (s *Service) CancelSubscription(ctx context.Context, customerEmail, reason string, immediate bool) (*Subscription, error) {
	_, sub, err := s.customerSubscription(ctx, customerEmail)
	if err != nil {
		return nil, err
	}

	state, err := s.provider.CancelSubscription(ctx, sub.ProviderSubscriptionID, immediate)
	if err != nil {
		return nil, fmt.Errorf("provider cancel: %w", err)
	}

	sub.Status = state.Status
	sub.CancelAtPeriodEnd = state.CancelAtPeriodEnd
	if immediate {
		now := time.Now()
		sub.CanceledAt = &now
	}
	if reason != "" {
		sub.CancellationReason = reason
	}
	if err := s.subs.Upsert(ctx, sub); err != nil {
		return nil, fmt.Errorf("update subscription: %w", err)
	}

	slog.InfoContext(ctx, "subscription cancelled", "app_id", s.appID, "email", customerEmail, "subscription_id", sub.ProviderSubscriptionID, "immediate", immediate)
	return sub, nil
}

// SetQuantity changes how many units the customer's subscription bills; the provider prorates the change. It returns
// ErrCustomerNotFound or ErrSubNotFound when there is no subscription, and leaves one that is no longer active as it is.
func (s *Service) SetQuantity(ctx context.Context, customerEmail string, quantity int64) (*Subscription, error) {
	cust, sub, err := s.customerSubscription(ctx, customerEmail)
	if err != nil {
		return nil, err
	}

	quantity = units(quantity)
	if !sub.IsActive() || units(sub.Quantity) == quantity {
		return sub, nil
	}
	state, err := s.provider.SetSubscriptionQuantity(ctx, sub.ProviderSubscriptionID, quantity)
	if err != nil {
		return nil, fmt.Errorf("provider update quantity: %w", err)
	}
	updated, err := s.upsertSubscription(ctx, cust.ID, state)
	if err != nil {
		return nil, fmt.Errorf("upsert subscription: %w", err)
	}
	slog.InfoContext(ctx, "subscription quantity changed", "app_id", s.appID, "email", customerEmail, "subscription_id", sub.ProviderSubscriptionID, "from", sub.Quantity, "to", updated.Quantity)
	return updated, nil
}

// CreatePortalSession returns the provider's customer portal URL. It returns ErrCustomerNotFound for a customer who
// never checked out.
func (s *Service) CreatePortalSession(ctx context.Context, customerEmail, returnURL, configurationID string) (string, error) {
	cust, err := s.customers.FindByEmailAndApp(ctx, customerEmail, s.appID)
	if errors.Is(err, ErrNotFound) {
		return "", ErrCustomerNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find customer: %w", err)
	}

	url, err := s.provider.CreatePortalSession(ctx, cust.ProviderCustomerID, returnURL, configurationID)
	if err != nil {
		return "", fmt.Errorf("create portal session: %w", err)
	}
	return url, nil
}

// GetPricesByIDs returns prices for the given ids. The store is a read-through cache: an id it lacks is fetched from
// the provider and cached.
func (s *Service) GetPricesByIDs(ctx context.Context, ids []string) ([]*Price, error) {
	prices, err := s.prices.FindByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("find prices by ids: %w", err)
	}

	cached := make(map[string]bool, len(prices))
	for _, p := range prices {
		cached[p.ID] = true
	}

	for _, id := range ids {
		if id == "" || cached[id] {
			continue
		}
		p, err := s.provider.GetPrice(ctx, id)
		if err != nil {
			slog.WarnContext(ctx, "price not found in cache or provider; skipping", "price_id", id, "error", err)
			continue
		}
		if err := s.prices.Upsert(ctx, p); err != nil {
			slog.WarnContext(ctx, "failed to cache fetched price", "price_id", id, "error", err)
		}
		prices = append(prices, p)
		cached[id] = true
	}
	return prices, nil
}

// CreatePromotionCode mints a promotion code on top of an existing coupon. It returns ErrPromotionExists when the code
// is taken.
func (s *Service) CreatePromotionCode(ctx context.Context, in PromotionCodeInput) (*PromotionCode, error) {
	pc, err := s.provider.CreatePromotionCode(ctx, in)
	if errors.Is(err, ErrPromotionExists) {
		return nil, ErrPromotionExists
	}
	if err != nil {
		return nil, fmt.Errorf("create promotion code: %w", err)
	}
	return pc, nil
}

// findOrCreateCustomer returns the app's customer for email, creating it at the provider and in the store when new.
func (s *Service) findOrCreateCustomer(ctx context.Context, email, name string) (*Customer, error) {
	cust, err := s.customers.FindByEmailAndApp(ctx, email, s.appID)
	if err == nil {
		return cust, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	providerID, err := s.provider.FindCustomer(ctx, email, s.appID)
	if err != nil {
		return nil, fmt.Errorf("find provider customer: %w", err)
	}
	if providerID == "" {
		providerID, err = s.provider.CreateCustomer(ctx, email, name, s.appID)
		if err != nil {
			return nil, fmt.Errorf("create provider customer: %w", err)
		}
	}

	cust = &Customer{ID: customerID(s.appID, email), Email: email, AppID: s.appID, Name: name, ProviderCustomerID: providerID}
	if err := s.customers.Upsert(ctx, cust); err != nil {
		return nil, fmt.Errorf("upsert customer: %w", err)
	}
	return cust, nil
}

// upsertSubscription stores the provider's state as the customer's subscription to the app; a customer has one.
func (s *Service) upsertSubscription(ctx context.Context, customerID string, st *SubscriptionState) (*Subscription, error) {
	sub := &Subscription{
		ID:                     customerID,
		CustomerID:             customerID,
		AppID:                  s.appID,
		ProviderSubscriptionID: st.ID,
		PriceID:                st.PriceID,
		Quantity:               st.Quantity,
		Status:                 st.Status,
		CancelAtPeriodEnd:      st.CancelAtPeriodEnd,
		CurrentPeriodStart:     timePtr(st.CurrentPeriodStart),
		CurrentPeriodEnd:       timePtr(st.CurrentPeriodEnd),
		TrialEnd:               timePtr(st.TrialEnd),
		CanceledAt:             timePtr(st.CanceledAt),
	}
	if err := s.subs.Upsert(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
