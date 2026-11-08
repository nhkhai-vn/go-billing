package billing_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	billing "github.com/nhkhai-vn/go-billing"
	"github.com/nhkhai-vn/go-billing/memory"
)

const app = "myapp"

var errDown = errors.New("down")

type usageCall struct {
	meter, customerID, identifier string
	value                         int64
}

type fakeProvider struct {
	existing      map[string]string
	findErr       error
	createErr     error
	created       []string
	checkout      *billing.CheckoutParams
	checkoutErr   error
	sessions      map[string]*billing.CheckoutState
	getCheckoutEr error
	portal        []string
	portalErr     error
	subs          map[string]*billing.SubscriptionState
	getSubErr     error
	getSubCalls   int
	cancelErr     error
	quantityErr   error
	quantityCalls int
	products      map[string]*billing.Product
	productErr    error
	productCalls  int
	prices        map[string]*billing.Price
	priceErr      error
	priceCalls    int
	promoErr      error
	usage         []usageCall
	usageErr      error
	event         *billing.WebhookEvent
	parseErr      error
}

func newProvider() *fakeProvider {
	return &fakeProvider{
		existing: map[string]string{},
		sessions: map[string]*billing.CheckoutState{},
		subs:     map[string]*billing.SubscriptionState{},
		products: map[string]*billing.Product{},
		prices:   map[string]*billing.Price{},
	}
}

func (p *fakeProvider) FindCustomer(_ context.Context, email, _ string) (string, error) {
	return p.existing[email], p.findErr
}

func (p *fakeProvider) CreateCustomer(_ context.Context, email, name, appID string) (string, error) {
	if p.createErr != nil {
		return "", p.createErr
	}
	p.created = append(p.created, appID+"|"+email+"|"+name)
	return fmt.Sprintf("cus_%d", len(p.created)), nil
}

func (p *fakeProvider) CreateCheckout(_ context.Context, params billing.CheckoutParams) (*billing.CheckoutSession, error) {
	if p.checkoutErr != nil {
		return nil, p.checkoutErr
	}
	p.checkout = &params
	return &billing.CheckoutSession{ID: "cs_1", URL: "https://pay.example/cs_1"}, nil
}

func (p *fakeProvider) GetCheckout(_ context.Context, sessionID string) (*billing.CheckoutState, error) {
	if p.getCheckoutEr != nil {
		return nil, p.getCheckoutEr
	}
	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, billing.ErrNotFound
	}
	return s, nil
}

func (p *fakeProvider) CreatePortalSession(_ context.Context, customerID, returnURL, configurationID string) (string, error) {
	if p.portalErr != nil {
		return "", p.portalErr
	}
	p.portal = []string{customerID, returnURL, configurationID}
	return "https://pay.example/portal", nil
}

func (p *fakeProvider) GetSubscription(_ context.Context, id string) (*billing.SubscriptionState, error) {
	p.getSubCalls++
	if p.getSubErr != nil {
		return nil, p.getSubErr
	}
	s, ok := p.subs[id]
	if !ok {
		return nil, billing.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (p *fakeProvider) CancelSubscription(_ context.Context, id string, immediate bool) (*billing.SubscriptionState, error) {
	if p.cancelErr != nil {
		return nil, p.cancelErr
	}
	s := &billing.SubscriptionState{ID: id, AppID: app, Status: billing.StatusActive, CancelAtPeriodEnd: true}
	if immediate {
		s.Status, s.CancelAtPeriodEnd = billing.StatusCanceled, false
	}
	return s, nil
}

func (p *fakeProvider) SetSubscriptionQuantity(_ context.Context, id string, quantity int64) (*billing.SubscriptionState, error) {
	p.quantityCalls++
	if p.quantityErr != nil {
		return nil, p.quantityErr
	}
	return &billing.SubscriptionState{ID: id, AppID: app, Status: billing.StatusActive, PriceID: "price_1", Quantity: quantity}, nil
}

func (p *fakeProvider) GetPrice(_ context.Context, id string) (*billing.Price, error) {
	p.priceCalls++
	if p.priceErr != nil {
		return nil, p.priceErr
	}
	pr, ok := p.prices[id]
	if !ok {
		return nil, billing.ErrNotFound
	}
	c := *pr
	return &c, nil
}

func (p *fakeProvider) GetProduct(_ context.Context, id string) (*billing.Product, error) {
	p.productCalls++
	if p.productErr != nil {
		return nil, p.productErr
	}
	pr, ok := p.products[id]
	if !ok {
		return nil, fmt.Errorf("get product %s: %w", id, billing.ErrNotFound)
	}
	return pr, nil
}

func (p *fakeProvider) CreatePromotionCode(_ context.Context, in billing.PromotionCodeInput) (*billing.PromotionCode, error) {
	if p.promoErr != nil {
		return nil, p.promoErr
	}
	return &billing.PromotionCode{Code: in.Code, PromotionID: "promo_1"}, nil
}

func (p *fakeProvider) ReportUsage(_ context.Context, meter, customerID string, value int64, identifier string) error {
	if p.usageErr != nil {
		return p.usageErr
	}
	p.usage = append(p.usage, usageCall{meter: meter, customerID: customerID, value: value, identifier: identifier})
	return nil
}

func (p *fakeProvider) ParseWebhook([]byte, http.Header) (*billing.WebhookEvent, error) {
	if p.parseErr != nil {
		return nil, p.parseErr
	}
	return p.event, nil
}

type recordingNotifier struct {
	sent []billing.Notification
	err  error
}

func (n *recordingNotifier) Notify(_ context.Context, msg billing.Notification) error {
	n.sent = append(n.sent, msg)
	return n.err
}

type env struct {
	svc      *billing.Service
	provider *fakeProvider
	store    billing.Store
	notifier *recordingNotifier
}

func setup(t *testing.T, wrap ...func(*billing.Store)) *env {
	t.Helper()
	e := &env{provider: newProvider(), store: memory.NewStore(), notifier: &recordingNotifier{}}
	for _, w := range wrap {
		w(&e.store)
	}
	svc, err := billing.New(billing.Config{AppID: app, Store: e.store, Provider: e.provider, Notifier: e.notifier})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.svc = svc
	return e
}

func (e *env) seedCustomer(t *testing.T, email, providerID string) *billing.Customer {
	t.Helper()
	c := &billing.Customer{ID: app + "|" + email, Email: email, AppID: app, ProviderCustomerID: providerID}
	if err := e.store.Customers.Upsert(context.Background(), c); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return c
}

func (e *env) seedSubscription(t *testing.T, providerSubID, status string, quantity int64) *billing.Subscription {
	t.Helper()
	id := app + "|a@example.com"
	s := &billing.Subscription{ID: id, CustomerID: id, AppID: app, ProviderSubscriptionID: providerSubID, PriceID: "price_1", Status: status, Quantity: quantity}
	if err := e.store.Subscriptions.Upsert(context.Background(), s); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return s
}

func (e *env) subscription(t *testing.T) *billing.Subscription {
	t.Helper()
	s, err := e.store.Subscriptions.FindByProviderID(context.Background(), "sub_1")
	if err != nil {
		t.Fatalf("find subscription: %v", err)
	}
	return s
}

type failingCustomers struct {
	billing.CustomerRepository
	err error
}

func (f failingCustomers) FindByEmailAndApp(context.Context, string, string) (*billing.Customer, error) {
	return nil, f.err
}

type failingSubscriptions struct {
	billing.SubscriptionRepository
	err error
}

func (f failingSubscriptions) FindByCustomerAndApp(context.Context, string, string) (*billing.Subscription, error) {
	return nil, f.err
}

func customersFail(s *billing.Store) { s.Customers = failingCustomers{s.Customers, errDown} }

func subscriptionsFail(s *billing.Store) {
	s.Subscriptions = failingSubscriptions{s.Subscriptions, errDown}
}

func TestNew_RequiresAppProviderAndStore(t *testing.T) {
	store, provider := memory.NewStore(), newProvider()
	without := func(f func(*billing.Store)) billing.Store { s := store; f(&s); return s }
	tests := map[string]billing.Config{
		"app":           {Store: store, Provider: provider},
		"provider":      {AppID: app, Store: store},
		"customers":     {AppID: app, Provider: provider, Store: without(func(s *billing.Store) { s.Customers = nil })},
		"subscriptions": {AppID: app, Provider: provider, Store: without(func(s *billing.Store) { s.Subscriptions = nil })},
		"events":        {AppID: app, Provider: provider, Store: without(func(s *billing.Store) { s.Events = nil })},
		"prices":        {AppID: app, Provider: provider, Store: without(func(s *billing.Store) { s.Prices = nil })},
	}
	for name, cfg := range tests {
		if _, err := billing.New(cfg); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
	if _, err := billing.New(billing.Config{AppID: app, Store: store, Provider: provider}); err != nil {
		t.Errorf("New without a notifier: %v", err)
	}
}
