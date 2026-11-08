package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	billing "github.com/nhkhai-vn/go-billing"
)

type captured struct {
	auth        string
	contentType string
	body        map[string]any
}

func server(t *testing.T, status int, reply string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		got.contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func attachment(t *testing.T, got *captured) map[string]any {
	t.Helper()
	atts, ok := got.body["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("missing attachments in %v", got.body)
	}
	return atts[0].(map[string]any)
}

func TestNotify_PostsToTheChannelWithTheBotToken(t *testing.T) {
	srv, got := server(t, http.StatusOK, `{"ok":true}`)
	n := New("xoxb-token", "C123").WithAPIURL(srv.URL)

	if err := n.Notify(context.Background(), billing.Notification{Title: "Hi", Text: "body", Warn: true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.auth != "Bearer xoxb-token" || got.body["channel"] != "C123" {
		t.Errorf("auth %q, body %v", got.auth, got.body)
	}
	att := attachment(t, got)
	if att["title"] != "Hi" || att["text"] != "body" || att["color"] != "warning" {
		t.Errorf("attachment = %v", att)
	}
}

func TestNotify_GoodColorWhenNotWarn(t *testing.T) {
	srv, got := server(t, http.StatusOK, `{"ok":true}`)
	if err := New("t", "C1").WithAPIURL(srv.URL).Notify(context.Background(), billing.Notification{Title: "ok"}); err != nil {
		t.Fatal(err)
	}
	if att := attachment(t, got); att["color"] != "good" {
		t.Errorf("color = %v, want good", att["color"])
	}
}

func TestNotify_SlackRejectionIsAnError(t *testing.T) {
	for _, slackError := range []string{"invalid_auth", "channel_not_found", "not_in_channel"} {
		srv, _ := server(t, http.StatusOK, `{"ok":false,"error":"`+slackError+`"}`)
		err := New("t", "C1").WithAPIURL(srv.URL).Notify(context.Background(), billing.Notification{Title: "x"})
		if err == nil {
			t.Errorf("%s: expected an error for ok=false", slackError)
		}
	}
}

func TestNotify_ErrorOnNon2xx(t *testing.T) {
	srv, _ := server(t, http.StatusInternalServerError, `{}`)
	if err := New("t", "C1").WithAPIURL(srv.URL).Notify(context.Background(), billing.Notification{Title: "x"}); err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestNotify_ErrorOnUnreadableReply(t *testing.T) {
	srv, _ := server(t, http.StatusOK, `not json`)
	if err := New("t", "C1").WithAPIURL(srv.URL).Notify(context.Background(), billing.Notification{Title: "x"}); err == nil {
		t.Fatal("expected error when the reply is not JSON")
	}
}
