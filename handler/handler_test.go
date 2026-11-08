package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

type stubService struct {
	checkoutIn   billing.CheckoutInput
	checkoutErr  error
	confirmErr   error
	sub          *billing.Subscription
	statusErr    error
	portalErr    error
	portalEmail  string
	portalReturn string
	portalConfig string
	webhookErr   error
	webhookBody  []byte
	webhookSig   http.Header
	webhookCalls int
	planIn       billing.CheckoutInput
	plan         *billing.Plan
	planErr      error
}

func (s *stubService) CreateCheckout(_ context.Context, in billing.CheckoutInput) (string, string, error) {
	s.checkoutIn = in
	return "https://checkout", "cs_1", s.checkoutErr
}

func (s *stubService) ConfirmCheckout(context.Context, string) (*billing.Subscription, error) {
	return s.sub, s.confirmErr
}

func (s *stubService) GetSubscriptionStatus(context.Context, string) (*billing.Subscription, error) {
	return s.sub, s.statusErr
}

func (s *stubService) CreatePortalSession(_ context.Context, email, returnURL, configID string) (string, error) {
	s.portalEmail, s.portalReturn, s.portalConfig = email, returnURL, configID
	return "https://portal", s.portalErr
}

func (s *stubService) HandleWebhook(_ context.Context, payload []byte, header http.Header) error {
	s.webhookCalls++
	s.webhookBody, s.webhookSig = payload, header
	return s.webhookErr
}

func (s *stubService) DescribePlan(_ context.Context, in billing.CheckoutInput) (*billing.Plan, error) {
	s.planIn = in
	return s.plan, s.planErr
}

func serve(h http.Handler, method, target, body string, customer *Customer) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if customer != nil {
		req = req.WithContext(WithCustomer(req.Context(), *customer))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var alice = &Customer{Email: "alice@example.com", Name: "Alice"}

func TestWebhook_PassesBodyAndHeaders(t *testing.T) {
	svc := &stubService{}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(`{"id":"evt_1"}`))
	req.Header.Set("Stripe-Signature", "t=1,v1=abc")
	rec := httptest.NewRecorder()
	Webhook(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || string(svc.webhookBody) != `{"id":"evt_1"}` || svc.webhookSig.Get("Stripe-Signature") != "t=1,v1=abc" {
		t.Errorf("status %d, body %q, headers %v", rec.Code, svc.webhookBody, svc.webhookSig)
	}
}

func TestWebhook_Rejections(t *testing.T) {
	tests := map[string]struct {
		sig  string
		body string
		err  error
		want int
	}{
		"bad signature":    {sig: "x", body: "{}", err: fmt.Errorf("parse webhook: %w", billing.ErrWebhookSignature), want: http.StatusBadRequest},
		"too large":        {sig: "x", body: strings.Repeat("a", maxWebhookBodySize+1), want: http.StatusRequestEntityTooLarge},
		"in progress":      {sig: "x", body: "{}", err: fmt.Errorf("claim event: %w", billing.ErrInProgress), want: http.StatusConflict},
		"processing fails": {sig: "x", body: "{}", err: errors.New("firestore down"), want: http.StatusInternalServerError},
	}
	for name, tc := range tests {
		svc := &stubService{webhookErr: tc.err}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		if tc.sig != "" {
			req.Header.Set("Stripe-Signature", tc.sig)
		}
		rec := httptest.NewRecorder()
		Webhook(svc).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
		if tc.want == http.StatusRequestEntityTooLarge && svc.webhookCalls != 0 {
			t.Errorf("%s: the service was called", name)
		}
	}
}

func TestWebhook_ProcessingFailureDoesNotLeakDetail(t *testing.T) {
	svc := &stubService{webhookErr: errors.New("secret firestore detail")}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Stripe-Signature", "x")
	rec := httptest.NewRecorder()
	Webhook(svc).ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("body leaks detail: %s", rec.Body.String())
	}
}

func TestCheckout_UsesProductOptionsAndSignedInCustomer(t *testing.T) {
	svc := &stubService{}
	opts := CheckoutOptions{ProductID: "prod_1", MeteredPriceID: "price_pages", SuccessURL: "https://app/ok", CancelURL: "https://app/no", TrialDays: 14, Locale: "ja", AllowPromotionCodes: true}
	rec := serve(Checkout(svc, opts), http.MethodPost, "/checkout", `{"price_id":"price_evil","success_url":"https://evil"}`, alice)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	in := svc.checkoutIn
	if in.CustomerEmail != "alice@example.com" || in.CustomerName != "Alice" || in.ProductID != "prod_1" || in.PriceID != "" || in.MeteredPriceID != "price_pages" ||
		in.SuccessURL != "https://app/ok" || in.TrialDays != 14 || !in.AllowPromotionCodes {
		t.Errorf("checkout input = %+v", in)
	}
	var got checkoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.CheckoutURL != "https://checkout" || got.SessionID != "cs_1" {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestCheckout_SellsTheCustomersQuantity(t *testing.T) {
	svc := &stubService{}
	rec := serve(Checkout(svc, CheckoutOptions{ProductID: "prod_1"}), http.MethodPost, "/", `{"quantity":99}`,
		&Customer{Email: "chain@example.com", Name: "Chain", Quantity: 3})
	if rec.Code != http.StatusOK || svc.checkoutIn.Quantity != 3 {
		t.Errorf("status %d, quantity %d, want 3", rec.Code, svc.checkoutIn.Quantity)
	}
}

func TestHandlers_NeedASignedInCustomer(t *testing.T) {
	svc := &stubService{}
	for name, h := range map[string]http.Handler{
		"checkout": Checkout(svc, CheckoutOptions{}),
		"status":   Status(svc),
		"portal":   Portal(svc, "https://ret", ""),
		"plan":     Plan(svc, CheckoutOptions{}),
	} {
		if rec := serve(h, http.MethodPost, "/", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without customer: status %d, want 401", name, rec.Code)
		}
		if rec := serve(h, http.MethodPost, "/", "", &Customer{}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with empty email: status %d, want 401", name, rec.Code)
		}
	}
}

func TestCheckout_ServiceErrorIs500WithoutDetail(t *testing.T) {
	svc := &stubService{checkoutErr: errors.New("stripe said no: secret")}
	rec := serve(Checkout(svc, CheckoutOptions{}), http.MethodPost, "/", "", alice)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPlan_DescribesTheCheckoutOptionsForTheCustomer(t *testing.T) {
	svc := &stubService{plan: &billing.Plan{
		Name: "SuguQ", PriceID: "price_1", Amount: 1000, Currency: "jpy", Interval: "month", IntervalCount: 1,
		Quantity: 2, TaxBehavior: "inclusive", TrialDays: 14,
	}}
	opts := CheckoutOptions{ProductID: "prod_1", TrialDays: 14, SuccessURL: "https://app/ok"}
	customer := &Customer{Email: "alice@example.com", Name: "Alice", Quantity: 2}
	rec := serve(Plan(svc, opts), http.MethodGet, "/plan?product_id=prod_evil", "", customer)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if in := svc.planIn; in.CustomerEmail != "alice@example.com" || in.ProductID != "prod_1" || in.TrialDays != 14 || in.Quantity != 2 {
		t.Errorf("plan input = %+v", in)
	}
	var got planResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := planResponse{Name: "SuguQ", PriceID: "price_1", UnitAmount: 1000, Currency: "jpy", Interval: "month", IntervalCount: 1, Quantity: 2, TaxBehavior: "inclusive", TrialDays: 14}
	if got != want {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}

func TestPlan_Errors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"no price on sale": {billing.ErrNoDefaultPrice, http.StatusNotFound},
		"stripe down":      {errors.New("stripe: secret detail"), http.StatusInternalServerError},
	}
	for name, tc := range tests {
		rec := serve(Plan(&stubService{planErr: tc.err}, CheckoutOptions{}), http.MethodGet, "/", "", alice)
		if rec.Code != tc.want || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestStatus(t *testing.T) {
	end := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		sub  *billing.Subscription
		want statusResponse
	}{
		"none":     {nil, statusResponse{Status: "none"}},
		"active":   {&billing.Subscription{Status: billing.StatusActive, CurrentPeriodEnd: &end}, statusResponse{HasSubscription: true, Active: true, Status: "active", CurrentPeriodEnd: &end}},
		"past due": {&billing.Subscription{Status: billing.StatusPastDue}, statusResponse{HasSubscription: true, Active: true, Status: "past_due"}},
		"canceled": {&billing.Subscription{Status: billing.StatusCanceled}, statusResponse{HasSubscription: true, Status: "canceled"}},
	}
	for name, tc := range tests {
		rec := serve(Status(&stubService{sub: tc.sub}), http.MethodGet, "/", "", alice)
		var got statusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rec.Code != http.StatusOK || got.HasSubscription != tc.want.HasSubscription || got.Active != tc.want.Active || got.Status != tc.want.Status {
			t.Errorf("%s: status %d, got %+v, want %+v", name, rec.Code, got, tc.want)
		}
	}
	if rec := serve(Status(&stubService{statusErr: errors.New("boom")}), http.MethodGet, "/", "", alice); rec.Code != http.StatusInternalServerError {
		t.Errorf("service error: status %d, want 500", rec.Code)
	}
}

func TestConfirm(t *testing.T) {
	sub := &billing.Subscription{Status: billing.StatusTrialing}
	tests := map[string]struct {
		target string
		svc    *stubService
		want   int
	}{
		"ok":              {"/confirm?session_id=cs_1", &stubService{sub: sub}, http.StatusOK},
		"no session":      {"/confirm", &stubService{}, http.StatusBadRequest},
		"payment pending": {"/confirm?session_id=cs_1", &stubService{confirmErr: billing.ErrPaymentPending}, http.StatusConflict},
		"another app":     {"/confirm?session_id=cs_1", &stubService{confirmErr: billing.ErrAppMismatch}, http.StatusNotFound},
		"service failure": {"/confirm?session_id=cs_1", &stubService{confirmErr: errors.New("boom")}, http.StatusInternalServerError},
	}
	for name, tc := range tests {
		if rec := serve(Confirm(tc.svc), http.MethodGet, tc.target, "", nil); rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
	}
}

func TestPortal(t *testing.T) {
	svc := &stubService{}
	rec := serve(Portal(svc, "https://app/billing", "bpc_1"), http.MethodPost, "/", "", alice)
	if rec.Code != http.StatusOK || svc.portalEmail != "alice@example.com" || svc.portalReturn != "https://app/billing" || svc.portalConfig != "bpc_1" {
		t.Errorf("status %d, email %q return %q config %q", rec.Code, svc.portalEmail, svc.portalReturn, svc.portalConfig)
	}
	if rec := serve(Portal(&stubService{portalErr: billing.ErrCustomerNotFound}, "r", ""), http.MethodPost, "/", "", alice); rec.Code != http.StatusNotFound {
		t.Errorf("unknown customer: status %d, want 404", rec.Code)
	}
	if rec := serve(Portal(&stubService{portalErr: errors.New("boom")}, "r", ""), http.MethodPost, "/", "", alice); rec.Code != http.StatusInternalServerError {
		t.Errorf("service error: status %d, want 500", rec.Code)
	}
}

func TestService_BillingServiceSatisfiesTheInterface(t *testing.T) {
	var _ Service = (*billing.Service)(nil)
}
