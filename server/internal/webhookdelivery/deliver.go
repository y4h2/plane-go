// Package webhookdelivery fires outbound webhook HTTP POSTs and records the
// delivery in webhook_logs. In Plane's Django backend this is the webhook_task
// Celery worker; here it runs in a bg.Dispatcher goroutine off the request path.
package webhookdelivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var client = &http.Client{Timeout: 4 * time.Second}

// eventColumn maps an event name to the boolean flag column on `webhooks` that
// opts a webhook in to that event.
var eventColumn = map[string]string{
	"issue": "issue", "module": "module", "cycle": "cycle",
	"issue_comment": "issue_comment", "project": "project",
}

// Fire delivers `event`/`action` with `data` to every active webhook in the
// workspace subscribed to that event, recording each attempt in webhook_logs.
// Safe to run from a background goroutine.
func Fire(ctx context.Context, pool *pgxpool.Pool, wsID uuid.UUID, event, action string, data map[string]any) {
	col, ok := eventColumn[event]
	if !ok {
		return
	}
	rows, err := pool.Query(ctx,
		`select id, url, secret_key from webhooks
		 where workspace_id=$1 and is_active and deleted_at is null and `+col+` = true`, wsID)
	if err != nil {
		return
	}
	type target struct {
		id     uuid.UUID
		url    string
		secret string
	}
	var targets []target
	for rows.Next() {
		var t target
		if rows.Scan(&t.id, &t.url, &t.secret) == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()

	for _, t := range targets {
		payload, _ := json.Marshal(map[string]any{
			"event":        event,
			"action":       action,
			"webhook_id":   t.id.String(),
			"workspace_id": wsID.String(),
			"data":         data,
		})
		headers, status, respBody := post(ctx, t.url, t.secret, event, payload)
		// QUIRK (matches Django's webhook_task): the column named request_method
		// actually stores the event ACTION ("created"/"updated"), not the HTTP verb.
		_, _ = pool.Exec(ctx, `
			insert into webhook_logs
			  (workspace_id, webhook, event_type, request_method, request_headers, request_body,
			   response_status, response_headers, response_body, retry_count)
			values ($1,$2,$3,$4,$5,$6,$7,'{}',$8,0)`,
			wsID, t.id, event, action, headers, string(payload),
			status, respBody)
	}
}

// post sends the signed payload and returns the request headers (as a string,
// for the log), the response status ("" on transport error) and a truncated
// response body.
func post(ctx context.Context, url, secret, event string, body []byte) (headers, status, respBody string) {
	sig := ""
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		sig = hex.EncodeToString(mac.Sum(nil))
	}
	hdr := map[string]string{
		"Content-Type":      "application/json",
		"User-Agent":        "Autopilot",
		"X-Plane-Event":     event,
		"X-Plane-Signature": sig,
	}
	hj, _ := json.Marshal(hdr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return string(hj), "", ""
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return string(hj), "", err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return string(hj), http.StatusText(resp.StatusCode), string(b)
}
