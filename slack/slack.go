// Package slack posts billing notifications to a Slack channel with a bot token (chat.postMessage).
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

const defaultAPIURL = "https://slack.com/api/chat.postMessage"

// Notifier posts to one channel with a bot token that has chat:write.
type Notifier struct {
	token   string
	channel string
	apiURL  string
	client  *http.Client
}

func New(token, channel string) *Notifier {
	return &Notifier{
		token:   token,
		channel: channel,
		apiURL:  defaultAPIURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// WithAPIURL points the notifier at another Slack API endpoint, for tests.
func (n *Notifier) WithAPIURL(url string) *Notifier {
	n.apiURL = url
	return n
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (n *Notifier) Notify(ctx context.Context, msg billing.Notification) error {
	color := "good"
	if msg.Warn {
		color = "warning"
	}
	body, err := json.Marshal(map[string]any{
		"channel": n.channel,
		"attachments": []map[string]any{
			{"color": color, "title": msg.Title, "text": msg.Text},
		},
	})
	if err != nil {
		return fmt.Errorf("encode slack message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+n.token)

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("post slack message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("slack returned status %d", resp.StatusCode)
	}
	// Slack answers 200 with ok=false for a bad token, an unknown channel or a bot that is not in the channel.
	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode slack response: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("slack rejected the message: %s", out.Error)
	}
	return nil
}
