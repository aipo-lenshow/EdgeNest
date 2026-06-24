// Package notify sends operational messages to Telegram. The functions are
// stateless — credentials are passed in per-call so the caller controls where
// they're stored.
//
// Failure modes are surfaced as plain errors; the caller decides whether a
// missed notification is worth alerting the operator about.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// SendTelegram posts a sendMessage call to the Bot API. chatID may be a
// numeric user/chat ID or "@channelusername".
func SendTelegram(ctx context.Context, botToken, chatID, text string) error {
	if botToken == "" {
		return fmt.Errorf("telegram bot token empty")
	}
	if chatID == "" {
		return fmt.Errorf("telegram chat_id empty")
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(botToken))
	body, _ := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
