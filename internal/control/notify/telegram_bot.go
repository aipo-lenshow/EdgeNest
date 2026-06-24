package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
)

// botClient long-polls with a timeout longer than the API's long-poll window
// so getUpdates doesn't trip the HTTP client's own deadline.
var botClient = &http.Client{Timeout: 70 * time.Second}

// TGUpdate is the minimal slice of a Telegram update we act on: a text message
// or an inline-keyboard callback, each carrying the originating chat ID (for
// the allowlist check) and the message ID (for callback answers).
type TGUpdate struct {
	UpdateID  int64
	ChatID    int64
	Text      string // message text ("" for a callback)
	Callback  string // callback_query.data ("" for a plain message)
	CallbackQ string // callback_query.id (to answer the callback)
	MessageID int64
}

// telegram API wire shapes (only the fields we read).
type tgGetUpdatesResp struct {
	OK     bool `json:"ok"`
	Result []struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
		CallbackQuery *struct {
			ID      string `json:"id"`
			Data    string `json:"data"`
			Message *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
	} `json:"result"`
}

// GetUpdates long-polls the Bot API. offset is the next update_id to fetch
// (last seen + 1); timeoutSec is the server-side long-poll window. Returns the
// parsed updates and the highest update_id seen (0 if none) so the caller can
// advance its offset durably.
func GetUpdates(ctx context.Context, botToken string, offset int64, timeoutSec int) ([]TGUpdate, int64, error) {
	if botToken == "" {
		return nil, 0, fmt.Errorf("telegram bot token empty")
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", url.PathEscape(botToken))
	body, _ := json.Marshal(map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := botClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("getUpdates: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, 0, fmt.Errorf("getUpdates %d: %s", resp.StatusCode, string(raw))
	}
	var parsed tgGetUpdatesResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, 0, fmt.Errorf("getUpdates decode: %w", err)
	}
	var out []TGUpdate
	var maxID int64
	for _, u := range parsed.Result {
		if u.UpdateID > maxID {
			maxID = u.UpdateID
		}
		switch {
		case u.Message != nil:
			out = append(out, TGUpdate{
				UpdateID:  u.UpdateID,
				ChatID:    u.Message.Chat.ID,
				Text:      u.Message.Text,
				MessageID: u.Message.MessageID,
			})
		case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
			out = append(out, TGUpdate{
				UpdateID:  u.UpdateID,
				ChatID:    u.CallbackQuery.Message.Chat.ID,
				Callback:  u.CallbackQuery.Data,
				CallbackQ: u.CallbackQuery.ID,
				MessageID: u.CallbackQuery.Message.MessageID,
			})
		}
	}
	return out, maxID, nil
}

// SendTelegramHTML posts a message with HTML parse mode (so replies can use
// <b>/<code> formatting). chatID is the numeric id as a string.
func SendTelegramHTML(ctx context.Context, botToken, chatID, html string) error {
	return sendMessage(ctx, botToken, map[string]any{
		"chat_id":                  chatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
}

// SendTelegramWithButtons sends an HTML message with a single-row inline
// keyboard. Each button is {label, callbackData}. Used for the destructive-
// command confirm prompts.
func SendTelegramWithButtons(ctx context.Context, botToken, chatID, html string, buttons [][2]string) error {
	return SendTelegramKeyboard(ctx, botToken, chatID, html, [][][2]string{buttons})
}

// SendTelegramKeyboard sends an HTML message with a multi-row inline keyboard.
// rows[i] is one row of {label, callbackData} buttons.
func SendTelegramKeyboard(ctx context.Context, botToken, chatID, html string, rows [][][2]string) error {
	kb := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]any, 0, len(row))
		for _, b := range row {
			r = append(r, map[string]any{"text": b[0], "callback_data": b[1]})
		}
		kb = append(kb, r)
	}
	return sendMessage(ctx, botToken, map[string]any{
		"chat_id":      chatID,
		"text":         html,
		"parse_mode":   "HTML",
		"reply_markup": map[string]any{"inline_keyboard": kb},
	})
}

// EditTelegramKeyboard edits an existing message's text + inline keyboard in
// place (editMessageText). Used to redraw a picker on pagination, toggle a
// multi-select option, or advance a wizard step without spamming new messages.
// rows == nil strips the keyboard (sends an empty inline_keyboard), e.g. to
// freeze a completed step's buttons.
func EditTelegramKeyboard(ctx context.Context, botToken, chatID string, messageID int64, html string, rows [][][2]string) error {
	kb := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]any, 0, len(row))
		for _, b := range row {
			r = append(r, map[string]any{"text": b[0], "callback_data": b[1]})
		}
		kb = append(kb, r)
	}
	return sendMessage(ctx, botToken, map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             map[string]any{"inline_keyboard": kb},
	}, "editMessageText")
}

// BotCommand is one entry in the Telegram command menu (the ☰ Menu button +
// "/" autocomplete). Command must be ASCII [a-z0-9_], 1-32 chars; description
// may be any text (localized).
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands registers the bot's command menu so the client shows tappable
// commands instead of requiring the user to type them. Idempotent — safe to
// call on every (re)start or language change.
func SetMyCommands(ctx context.Context, botToken string, cmds []BotCommand) error {
	return sendMessage(ctx, botToken, map[string]any{"commands": cmds}, "setMyCommands")
}

// SendTelegramPhoto uploads a PNG (or other image) via sendPhoto multipart.
// caption may be empty; parseMode applies to the caption (HTML). Used to push
// the subscription QR code as an inline image.
func SendTelegramPhoto(ctx context.Context, botToken, chatID string, png []byte, caption string) error {
	if botToken == "" {
		return fmt.Errorf("telegram bot token empty")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", chatID)
	if caption != "" {
		_ = mw.WriteField("caption", caption)
		_ = mw.WriteField("parse_mode", "HTML")
	}
	fw, err := mw.CreateFormFile("photo", "qr.png")
	if err != nil {
		return err
	}
	if _, err := fw.Write(png); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", url.PathEscape(botToken))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendPhoto: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sendPhoto %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

// AnswerCallback acknowledges a callback_query so Telegram stops showing the
// button's loading spinner. Best-effort.
func AnswerCallback(ctx context.Context, botToken, callbackID, text string) error {
	return sendMessage(ctx, botToken, map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
	}, "answerCallbackQuery")
}

// sendMessage POSTs a JSON payload to a Bot API method (default sendMessage).
func sendMessage(ctx context.Context, botToken string, payload map[string]any, method ...string) error {
	if botToken == "" {
		return fmt.Errorf("telegram bot token empty")
	}
	m := "sendMessage"
	if len(method) > 0 {
		m = method[0]
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s", url.PathEscape(botToken), m)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", m, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %d: %s", m, resp.StatusCode, string(raw))
	}
	return nil
}
