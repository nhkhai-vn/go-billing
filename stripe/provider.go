// Package stripe is the Stripe Provider for billing.
package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
	gostripe "github.com/stripe/stripe-go/v87"
)

var _ billing.Provider = (*Provider)(nil)

// checkoutExpiry is how long a customer has to finish a checkout.
const checkoutExpiry = 30 * time.Minute

// Provider is billing.Provider for one Stripe API key. It holds its own SDK client, so Providers with different keys
// can coexist in a process.
type Provider struct {
	api            *gostripe.Client
	webhookSecrets []string
}

// New returns a Provider that calls Stripe with secretKey and accepts webhooks signed by any of webhookSecrets.
func New(secretKey string, webhookSecrets []string) *Provider {
	return &Provider{api: gostripe.NewClient(secretKey), webhookSecrets: webhookSecrets}
}

// FindCustomer returns the customer of email whose app_id metadata is appID. A customer of another app, or one with
// no app_id, is never reused.
func (p *Provider) FindCustomer(ctx context.Context, email, appID string) (string, error) {
	list := p.api.V1Customers.List(ctx, &gostripe.CustomerListParams{Email: gostripe.String(email)})
	for cust, err := range list.All(ctx) {
		if err != nil {
			return "", fmt.Errorf("list customers: %w", err)
		}
		if cust.Metadata["app_id"] == appID {
			return cust.ID, nil
		}
	}
	return "", nil
}

// CreateCustomer creates a customer stamped with appID.
func (p *Provider) CreateCustomer(ctx context.Context, email, name, appID string) (string, error) {
	cust, err := p.api.V1Customers.Create(ctx, &gostripe.CustomerCreateParams{
		Email:    gostripe.String(email),
		Name:     gostripe.String(name),
		Metadata: map[string]string{"app_id": appID},
	})
	if err != nil {
		return "", fmt.Errorf("create customer: %w", err)
	}
	return cust.ID, nil
}

// CreateCheckout creates a subscription checkout session paid by card, with automatic tax.
func (p *Provider) CreateCheckout(ctx context.Context, in billing.CheckoutParams) (*billing.CheckoutSession, error) {
	lineItems := []*gostripe.CheckoutSessionCreateLineItemParams{
		{Price: gostripe.String(in.PriceID), Quantity: gostripe.Int64(max(in.Quantity, 1))},
	}
	if in.MeteredPriceID != "" {
		lineItems = append(lineItems, &gostripe.CheckoutSessionCreateLineItemParams{Price: gostripe.String(in.MeteredPriceID)})
	}
	if in.SetupPriceID != "" {
		lineItems = append(lineItems, &gostripe.CheckoutSessionCreateLineItemParams{
			Price:    gostripe.String(in.SetupPriceID),
			Quantity: gostripe.Int64(1),
		})
	}

	// Session metadata does not propagate to the subscription, so app_id is stamped on both.
	subscriptionData := &gostripe.CheckoutSessionCreateSubscriptionDataParams{
		Metadata: map[string]string{"app_id": in.AppID},
	}
	if in.TrialDays > 0 {
		subscriptionData.TrialPeriodDays = gostripe.Int64(int64(in.TrialDays))
	}

	params := &gostripe.CheckoutSessionCreateParams{
		Customer:                  gostripe.String(in.CustomerID),
		Mode:                      gostripe.String(string(gostripe.CheckoutSessionModeSubscription)),
		SuccessURL:                gostripe.String(in.SuccessURL),
		CancelURL:                 gostripe.String(in.CancelURL),
		AllowedPaymentMethodTypes: gostripe.StringSlice([]string{"card"}),
		LineItems:                 lineItems,
		AutomaticTax:              &gostripe.CheckoutSessionCreateAutomaticTaxParams{Enabled: gostripe.Bool(true)},
		BillingAddressCollection:  gostripe.String(string(gostripe.CheckoutSessionBillingAddressCollectionAuto)),
		AllowPromotionCodes:       gostripe.Bool(in.AllowPromotionCodes),
		CustomerUpdate:            &gostripe.CheckoutSessionCreateCustomerUpdateParams{Address: gostripe.String("auto"), Name: gostripe.String("auto")},
		ExpiresAt:                 gostripe.Int64(time.Now().Add(checkoutExpiry).Unix()),
		Metadata:                  map[string]string{"app_id": in.AppID, "customer_email": in.CustomerEmail},
		SubscriptionData:          subscriptionData,
	}
	if in.Locale != "" {
		params.Locale = gostripe.String(in.Locale)
	}

	sess, err := p.api.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create checkout session: %w", err)
	}
	return &billing.CheckoutSession{ID: sess.ID, URL: sess.URL}, nil
}

// GetCheckout returns the checkout session with its subscription expanded.
func (p *Provider) GetCheckout(ctx context.Context, sessionID string) (*billing.CheckoutState, error) {
	sess, err := p.api.V1CheckoutSessions.Retrieve(ctx, sessionID, &gostripe.CheckoutSessionRetrieveParams{
		Expand: gostripe.StringSlice([]string{"subscription"}),
	})
	if err != nil {
		return nil, fmt.Errorf("get checkout session: %w", err)
	}
	state := checkoutState(sess)
	if sess.Subscription != nil {
		state.Subscription = subscriptionState(sess.Subscription)
	}
	return state, nil
}

// CreatePortalSession returns the URL of a billing portal session. An empty configurationID uses the account default.
func (p *Provider) CreatePortalSession(ctx context.Context, customerID, returnURL, configurationID string) (string, error) {
	params := &gostripe.BillingPortalSessionCreateParams{
		Customer:  gostripe.String(customerID),
		ReturnURL: gostripe.String(returnURL),
	}
	// A product's own portal configuration scopes cancel and update per product on a shared account.
	if configurationID != "" {
		params.Configuration = gostripe.String(configurationID)
	}

	sess, err := p.api.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("create portal session: %w", err)
	}
	return sess.URL, nil
}

// GetSubscription returns the subscription's current state.
func (p *Provider) GetSubscription(ctx context.Context, subscriptionID string) (*billing.SubscriptionState, error) {
	sub, err := p.api.V1Subscriptions.Retrieve(ctx, subscriptionID, &gostripe.SubscriptionRetrieveParams{})
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return subscriptionState(sub), nil
}

// CancelSubscription cancels now when immediate, else at the end of the current period.
func (p *Provider) CancelSubscription(ctx context.Context, subscriptionID string, immediate bool) (*billing.SubscriptionState, error) {
	if immediate {
		sub, err := p.api.V1Subscriptions.Cancel(ctx, subscriptionID, &gostripe.SubscriptionCancelParams{})
		if err != nil {
			return nil, fmt.Errorf("cancel subscription: %w", err)
		}
		return subscriptionState(sub), nil
	}

	sub, err := p.api.V1Subscriptions.Update(ctx, subscriptionID, &gostripe.SubscriptionUpdateParams{
		CancelAtPeriodEnd: gostripe.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("set cancel at period end: %w", err)
	}
	return subscriptionState(sub), nil
}

// SetSubscriptionQuantity sets the quantity of the subscription's recurring item, which checkout creates as its only
// item, with prorations for the rest of the period.
func (p *Provider) SetSubscriptionQuantity(ctx context.Context, subscriptionID string, quantity int64) (*billing.SubscriptionState, error) {
	cur, err := p.api.V1Subscriptions.Retrieve(ctx, subscriptionID, &gostripe.SubscriptionRetrieveParams{})
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	if cur.Items == nil || len(cur.Items.Data) != 1 {
		return nil, fmt.Errorf("subscription %s: want one item to set its quantity", subscriptionID)
	}

	sub, err := p.api.V1Subscriptions.Update(ctx, subscriptionID, &gostripe.SubscriptionUpdateParams{
		Items: []*gostripe.SubscriptionUpdateItemParams{
			{ID: gostripe.String(cur.Items.Data[0].ID), Quantity: gostripe.Int64(quantity)},
		},
		ProrationBehavior: gostripe.String("create_prorations"),
	})
	if err != nil {
		return nil, fmt.Errorf("update subscription quantity: %w", err)
	}
	return subscriptionState(sub), nil
}

// GetPrice returns the price.
func (p *Provider) GetPrice(ctx context.Context, priceID string) (*billing.Price, error) {
	sp, err := p.api.V1Prices.Retrieve(ctx, priceID, &gostripe.PriceRetrieveParams{})
	if err != nil {
		return nil, fmt.Errorf("get price: %w", err)
	}
	return price(sp), nil
}

// GetProduct returns the product, or an error wrapping billing.ErrNotFound when Stripe has none.
func (p *Provider) GetProduct(ctx context.Context, productID string) (*billing.Product, error) {
	sp, err := p.api.V1Products.Retrieve(ctx, productID, &gostripe.ProductRetrieveParams{})
	var se *gostripe.Error
	if errors.As(err, &se) && se.HTTPStatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("get product %s: %w", productID, billing.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get product: %w", err)
	}
	return product(sp), nil
}

// CreatePromotionCode creates an active promotion code on an existing coupon. It returns an error wrapping
// billing.ErrPromotionExists when the code is taken.
func (p *Provider) CreatePromotionCode(ctx context.Context, in billing.PromotionCodeInput) (*billing.PromotionCode, error) {
	params := &gostripe.PromotionCodeCreateParams{
		Code:   gostripe.String(in.Code),
		Active: gostripe.Bool(true),
		Promotion: &gostripe.PromotionCodeCreatePromotionParams{
			Type:   gostripe.String("coupon"),
			Coupon: gostripe.String(in.Coupon),
		},
	}
	if in.FirstTimeTransaction {
		params.Restrictions = &gostripe.PromotionCodeCreateRestrictionsParams{
			FirstTimeTransaction: gostripe.Bool(true),
		}
	}
	if len(in.Metadata) > 0 {
		params.Metadata = in.Metadata
	}

	pc, err := p.api.V1PromotionCodes.Create(ctx, params)
	// Stripe has no error code for a taken promotion code; its message is the only signal.
	var se *gostripe.Error
	if errors.As(err, &se) && strings.Contains(se.Msg, "already exists") {
		return nil, fmt.Errorf("create promotion code %s: %w", in.Code, billing.ErrPromotionExists)
	}
	if err != nil {
		return nil, fmt.Errorf("create promotion code: %w", err)
	}
	return &billing.PromotionCode{Code: pc.Code, PromotionID: pc.ID, TimesRedeemed: pc.TimesRedeemed}, nil
}

// ReportUsage sends a meter event; Stripe counts a repeated identifier once.
func (p *Provider) ReportUsage(ctx context.Context, meter, customerID string, value int64, identifier string) error {
	_, err := p.api.V1BillingMeterEvents.Create(ctx, &gostripe.BillingMeterEventCreateParams{
		EventName:  gostripe.String(meter),
		Identifier: gostripe.String(identifier),
		Payload:    map[string]string{"stripe_customer_id": customerID, "value": strconv.FormatInt(value, 10)},
	})
	if err != nil {
		return fmt.Errorf("create meter event: %w", err)
	}
	return nil
}
