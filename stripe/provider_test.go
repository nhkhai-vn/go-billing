package stripe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
	gostripe "github.com/stripe/stripe-go/v87"
)

type recorded struct {
	auth   string
	method string
	path   string
	form   url.Values
	query  url.Values
}

func fakeStripe(t *testing.T, reply string) (*httptest.Server, func() []recorded) {
	t.Helper()
	return fakeStripeStatus(t, http.StatusOK, reply)
}

func fakeStripeStatus(t *testing.T, status int, reply string) (*httptest.Server, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		got = append(got, recorded{auth: r.Header.Get("Authorization"), method: r.Method, path: r.URL.Path, form: r.PostForm, query: r.URL.Query()})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), got...)
	}
}

func providerFor(srv *httptest.Server, key string) *Provider {
	backend := gostripe.GetBackendWithConfig(gostripe.APIBackend, &gostripe.BackendConfig{
		URL:           gostripe.String(srv.URL),
		HTTPClient:    srv.Client(),
		LeveledLogger: &gostripe.LeveledLogger{Level: gostripe.LevelNull},
	})
	return &Provider{api: gostripe.NewClient(key, gostripe.WithBackends(&gostripe.Backends{API: backend}))}
}

func wantForm(t *testing.T, f url.Values, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if f.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, f.Get(k), v)
		}
	}
}

func TestProviders_KeepTheirOwnKeys(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"price_1","object":"price"}`)
	a, b := providerFor(srv, "sk_test_a"), providerFor(srv, "sk_test_b")

	var wg sync.WaitGroup
	for _, p := range []*Provider{a, b, a, b} {
		wg.Go(func() {
			if _, err := p.GetPrice(context.Background(), "price_1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	counts := map[string]int{}
	for _, r := range requests() {
		counts[r.auth]++
	}
	if counts["Bearer sk_test_a"] != 2 || counts["Bearer sk_test_b"] != 2 || len(counts) != 2 {
		t.Errorf("requests by key = %v, want two per provider", counts)
	}
	if gostripe.Key != "" {
		t.Errorf("global stripe.Key was set to %q", gostripe.Key)
	}
}

func TestNew_DoesNotTouchTheGlobalKey(t *testing.T) {
	New("sk_test_x", nil)
	if gostripe.Key != "" {
		t.Errorf("global stripe.Key = %q", gostripe.Key)
	}
}

func TestFindCustomer_MatchesApp(t *testing.T) {
	srv, requests := fakeStripe(t, `{"object":"list","url":"/v1/customers","has_more":false,"data":[
		{"id":"cus_legacy","object":"customer","metadata":{}},
		{"id":"cus_other","object":"customer","metadata":{"app_id":"other"}},
		{"id":"cus_queue","object":"customer","metadata":{"app_id":"queue"}}]}`)
	id, err := providerFor(srv, "sk_test_a").FindCustomer(context.Background(), "u@example.com", "queue")
	if err != nil || id != "cus_queue" {
		t.Fatalf("id %q, err %v, want cus_queue", id, err)
	}
	if q := requests()[0].query; q.Get("email") != "u@example.com" {
		t.Errorf("query = %v", q)
	}
}

func TestFindCustomer_NoneOfTheApp(t *testing.T) {
	srv, _ := fakeStripe(t, `{"object":"list","url":"/v1/customers","has_more":false,"data":[
		{"id":"cus_other","object":"customer","metadata":{"app_id":"other"}}]}`)
	id, err := providerFor(srv, "sk_test_a").FindCustomer(context.Background(), "u@example.com", "queue")
	if err != nil || id != "" {
		t.Errorf("id %q, err %v, want empty and no error", id, err)
	}
}

func TestCreateCustomer_StampsApp(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"cus_1","object":"customer"}`)
	id, err := providerFor(srv, "sk_test_a").CreateCustomer(context.Background(), "u@example.com", "U", "queue")
	if err != nil || id != "cus_1" {
		t.Fatalf("id %q, err %v", id, err)
	}
	wantForm(t, requests()[0].form, map[string]string{"email": "u@example.com", "name": "U", "metadata[app_id]": "queue"})
}

func TestCreateCheckout_StampsAppOnSessionAndSubscription(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"cs_1","object":"checkout.session","url":"https://checkout"}`)
	sess, err := providerFor(srv, "sk_test_a").CreateCheckout(context.Background(), billing.CheckoutParams{
		CustomerID: "cus_1", PriceID: "price_1", Quantity: 3, SetupPriceID: "price_setup", SuccessURL: "https://ok", CancelURL: "https://no",
		AppID: "queue", CustomerEmail: "u@example.com", TrialDays: 14, AllowPromotionCodes: true, Locale: "ja",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "cs_1" || sess.URL != "https://checkout" {
		t.Errorf("session = %+v", sess)
	}
	reqs := requests()
	if len(reqs) != 1 || reqs[0].path != "/v1/checkout/sessions" {
		t.Fatalf("requests = %+v", reqs)
	}
	f := reqs[0].form
	wantForm(t, f, map[string]string{
		"customer":                             "cus_1",
		"mode":                                 "subscription",
		"metadata[app_id]":                     "queue",
		"metadata[customer_email]":             "u@example.com",
		"subscription_data[metadata][app_id]":  "queue",
		"subscription_data[trial_period_days]": "14",
		"line_items[0][price]":                 "price_1",
		"line_items[0][quantity]":              "3",
		"line_items[1][quantity]":              "1",
		"line_items[1][price]":                 "price_setup",
		"automatic_tax[enabled]":               "true",
		"allowed_payment_method_types[0]":      "card",
		"allow_promotion_codes":                "true",
		"locale":                               "ja",
	})
	expires, err := strconv.ParseInt(f.Get("expires_at"), 10, 64)
	if err != nil {
		t.Fatalf("expires_at = %q", f.Get("expires_at"))
	}
	if d := time.Until(time.Unix(expires, 0)); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("expires in %v, want 30m", d)
	}
}

func TestCreateCheckout_MeteredPriceHasNoQuantity(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"cs_1","object":"checkout.session"}`)
	_, err := providerFor(srv, "sk_test_a").CreateCheckout(context.Background(), billing.CheckoutParams{
		CustomerID: "cus_1", PriceID: "price_1", MeteredPriceID: "price_metered", AppID: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	f := requests()[0].form
	wantForm(t, f, map[string]string{
		"line_items[0][quantity]": "1",
		"line_items[1][price]":    "price_metered",
		"line_items[1][quantity]": "",
	})
	if f.Has("subscription_data[trial_period_days]") || f.Has("locale") {
		t.Errorf("trial or locale set without TrialDays or Locale: %v", f)
	}
}

func TestGetCheckout_FillsSubscription(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"cs_1","object":"checkout.session","payment_status":"paid","customer":"cus_1",
		"metadata":{"app_id":"queue","customer_email":"u@example.com"},
		"subscription":{"id":"sub_1","object":"subscription","status":"active","metadata":{"app_id":"queue"},
			"items":{"object":"list","data":[{"id":"si_1","object":"subscription_item","quantity":2,"price":{"id":"price_1","object":"price"}}]}}}`)
	got, err := providerFor(srv, "sk_test_a").GetCheckout(context.Background(), "cs_1")
	if err != nil {
		t.Fatal(err)
	}
	if q := requests()[0].query; q.Get("expand[0]") != "subscription" {
		t.Errorf("query = %v, want expand subscription", q)
	}
	if got.ID != "cs_1" || got.AppID != "queue" || got.CustomerEmail != "u@example.com" || got.CustomerID != "cus_1" || !got.Paid || got.SubscriptionID != "sub_1" {
		t.Errorf("checkout = %+v", got)
	}
	if s := got.Subscription; s == nil || s.ID != "sub_1" || s.Status != "active" || s.PriceID != "price_1" || s.Quantity != 2 || s.AppID != "queue" {
		t.Errorf("subscription = %+v", got.Subscription)
	}
}

func TestCreatePortalSession_PassesConfiguration(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"bps_1","object":"billing_portal.session","url":"https://portal"}`)
	p := providerFor(srv, "sk_test_a")
	u, err := p.CreatePortalSession(context.Background(), "cus_1", "https://back", "bpc_1")
	if err != nil || u != "https://portal" {
		t.Fatalf("url %q, err %v", u, err)
	}
	if _, err := p.CreatePortalSession(context.Background(), "cus_1", "https://back", ""); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	wantForm(t, reqs[0].form, map[string]string{"customer": "cus_1", "return_url": "https://back", "configuration": "bpc_1"})
	if reqs[1].form.Has("configuration") {
		t.Errorf("empty configuration was sent: %v", reqs[1].form)
	}
}

func TestCancelSubscription_SoftSetsCancelAtPeriodEnd(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"sub_1","object":"subscription","status":"active","cancel_at_period_end":true}`)
	sub, err := providerFor(srv, "sk_test_a").CancelSubscription(context.Background(), "sub_1", false)
	if err != nil || !sub.CancelAtPeriodEnd {
		t.Fatalf("sub %+v, err %v", sub, err)
	}
	if r := requests()[0]; r.method != http.MethodPost || r.path != "/v1/subscriptions/sub_1" || r.form.Get("cancel_at_period_end") != "true" {
		t.Errorf("request = %+v", r)
	}
}

func TestCancelSubscription_ImmediateCancels(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"sub_1","object":"subscription","status":"canceled","canceled_at":1700000000}`)
	sub, err := providerFor(srv, "sk_test_a").CancelSubscription(context.Background(), "sub_1", true)
	if err != nil || sub.Status != "canceled" || !sub.CanceledAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("sub %+v, err %v", sub, err)
	}
	if r := requests()[0]; r.method != http.MethodDelete || r.path != "/v1/subscriptions/sub_1" {
		t.Errorf("request = %+v", r)
	}
}

func TestSetSubscriptionQuantity_ProratesTheOnlyItem(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"sub_1","object":"subscription","status":"active","items":{"object":"list","data":[{"id":"si_1","object":"subscription_item","quantity":3}]}}`)
	sub, err := providerFor(srv, "sk_test_a").SetSubscriptionQuantity(context.Background(), "sub_1", 3)
	if err != nil || sub.Quantity != 3 {
		t.Fatalf("sub %+v, err %v", sub, err)
	}
	reqs := requests()
	if len(reqs) != 2 || reqs[1].path != "/v1/subscriptions/sub_1" {
		t.Fatalf("requests = %+v", reqs)
	}
	wantForm(t, reqs[1].form, map[string]string{"items[0][id]": "si_1", "items[0][quantity]": "3", "proration_behavior": "create_prorations"})
}

func TestSetSubscriptionQuantity_RefusesSeveralItems(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"sub_1","object":"subscription","items":{"object":"list","data":[{"id":"si_1"},{"id":"si_2"}]}}`)
	if _, err := providerFor(srv, "sk_test_a").SetSubscriptionQuantity(context.Background(), "sub_1", 3); err == nil {
		t.Fatal("want an error")
	}
	if n := len(requests()); n != 1 {
		t.Errorf("%d requests, want only the read", n)
	}
}

func TestGetPrice_Converts(t *testing.T) {
	srv, _ := fakeStripe(t, `{"id":"price_1","object":"price","product":"prod_1","unit_amount":1000,"currency":"jpy",
		"recurring":{"interval":"month","interval_count":3},"active":true,"nickname":"Pro","type":"recurring","tax_behavior":"inclusive"}`)
	got, err := providerFor(srv, "sk_test_a").GetPrice(context.Background(), "price_1")
	if err != nil {
		t.Fatal(err)
	}
	want := billing.Price{ID: "price_1", ProductID: "prod_1", Amount: 1000, Currency: "jpy", Interval: "month", IntervalCount: 3, Active: true, Nickname: "Pro", Type: "recurring", TaxBehavior: "inclusive"}
	if *got != want {
		t.Errorf("price = %+v, want %+v", *got, want)
	}
}

func TestGetPrice_OneTimeHasNoInterval(t *testing.T) {
	srv, _ := fakeStripe(t, `{"id":"price_setup","object":"price","unit_amount":500,"currency":"usd","type":"one_time"}`)
	got, err := providerFor(srv, "sk_test_a").GetPrice(context.Background(), "price_setup")
	if err != nil {
		t.Fatal(err)
	}
	if got.Interval != "" || got.IntervalCount != 0 || got.ProductID != "" || got.Type != "one_time" {
		t.Errorf("price = %+v", got)
	}
}

func TestGetProduct_Converts(t *testing.T) {
	srv, _ := fakeStripe(t, `{"id":"prod_1","object":"product","name":"Queue","active":true,"default_price":"price_1"}`)
	got, err := providerFor(srv, "sk_test_a").GetProduct(context.Background(), "prod_1")
	if err != nil {
		t.Fatal(err)
	}
	if *got != (billing.Product{ID: "prod_1", Name: "Queue", Active: true, DefaultPriceID: "price_1"}) {
		t.Errorf("product = %+v", got)
	}
}

func TestGetProduct_NotFoundWrapsErrNotFound(t *testing.T) {
	srv, _ := fakeStripeStatus(t, http.StatusNotFound, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such product"}}`)
	_, err := providerFor(srv, "sk_test_a").GetProduct(context.Background(), "prod_x")
	if !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("err = %v, want billing.ErrNotFound", err)
	}
}

func TestGetProduct_OtherErrorIsNotNotFound(t *testing.T) {
	srv, _ := fakeStripeStatus(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"bad"}}`)
	_, err := providerFor(srv, "sk_test_a").GetProduct(context.Background(), "prod_x")
	if err == nil || errors.Is(err, billing.ErrNotFound) {
		t.Errorf("err = %v, want an error other than billing.ErrNotFound", err)
	}
}

func TestCreatePromotionCode_SendsCouponAndRestrictions(t *testing.T) {
	srv, requests := fakeStripe(t, `{"id":"promo_1","object":"promotion_code","code":"FRIEND","times_redeemed":0}`)
	got, err := providerFor(srv, "sk_test_a").CreatePromotionCode(context.Background(), billing.PromotionCodeInput{
		Coupon: "coupon_1", Code: "FRIEND", FirstTimeTransaction: true, Metadata: map[string]string{"referrer": "u1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if *got != (billing.PromotionCode{Code: "FRIEND", PromotionID: "promo_1"}) {
		t.Errorf("promotion code = %+v", got)
	}
	wantForm(t, requests()[0].form, map[string]string{
		"code":                                 "FRIEND",
		"active":                               "true",
		"promotion[type]":                      "coupon",
		"promotion[coupon]":                    "coupon_1",
		"restrictions[first_time_transaction]": "true",
		"metadata[referrer]":                   "u1",
	})
}

func TestCreatePromotionCode_ExistsWrapsErrPromotionExists(t *testing.T) {
	srv, _ := fakeStripeStatus(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"An active promotion code with `+"`code: FRIEND`"+` already exists."}}`)
	_, err := providerFor(srv, "sk_test_a").CreatePromotionCode(context.Background(), billing.PromotionCodeInput{Coupon: "coupon_1", Code: "FRIEND"})
	if !errors.Is(err, billing.ErrPromotionExists) {
		t.Errorf("err = %v, want billing.ErrPromotionExists", err)
	}
}

func TestCreatePromotionCode_OtherErrorIsNotExists(t *testing.T) {
	srv, _ := fakeStripeStatus(t, http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"No such coupon"}}`)
	_, err := providerFor(srv, "sk_test_a").CreatePromotionCode(context.Background(), billing.PromotionCodeInput{Coupon: "coupon_x", Code: "FRIEND"})
	if err == nil || errors.Is(err, billing.ErrPromotionExists) {
		t.Errorf("err = %v, want an error other than billing.ErrPromotionExists", err)
	}
}

func TestReportUsage_SendsMeterEvent(t *testing.T) {
	srv, requests := fakeStripe(t, `{"object":"billing.meter_event","event_name":"pages"}`)
	if err := providerFor(srv, "sk_test_a").ReportUsage(context.Background(), "pages", "cus_1", 12, "job_1"); err != nil {
		t.Fatal(err)
	}
	r := requests()[0]
	if r.path != "/v1/billing/meter_events" {
		t.Errorf("path = %s", r.path)
	}
	wantForm(t, r.form, map[string]string{
		"event_name":                  "pages",
		"identifier":                  "job_1",
		"payload[stripe_customer_id]": "cus_1",
		"payload[value]":              "12",
	})
}
