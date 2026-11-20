// Package feishuauth implements Feishu's OAuth device-authorization flow
// (begin / poll): a caller shows the user a QR code, the user scans it with
// the Feishu app, and the flow hands back a freshly provisioned App ID/App
// Secret — no need to open the developer console and create a custom app by
// hand first.
package feishuauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const feishuBase = "https://accounts.feishu.cn"

// registrationEndpoint and postClient are the device-flow HTTP seam. Tests
// point them at a local server; production uses Feishu's registration URL.
var (
	registrationEndpoint = feishuBase + "/oauth/v1/app/registration"
	postClient           = http.DefaultClient
)

// BeginResponse is the result of starting a device-authorization request.
type BeginResponse struct {
	DeviceCode          string `json:"device_code"`
	VerificationURIComp string `json:"verification_uri_complete"` // QR code content
	UserCode            string `json:"user_code"`
	Interval            int    `json:"interval"`   // suggested poll interval, seconds
	ExpiresIn           int    `json:"expires_in"` // expiry, seconds
}

// PollResponse is the result of one poll for the device-authorization
// outcome.
type PollResponse struct {
	ClientID     string `json:"client_id"`     // app_id
	ClientSecret string `json:"client_secret"` // app_secret
	UserInfo     any    `json:"user_info"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Begin starts a device-authorization registration, returning the QR
// content and poll parameters.
func Begin() (*BeginResponse, error) {
	form := url.Values{
		"action":            {"begin"},
		"archetype":         {"PersonalAgent"},
		"auth_method":       {"client_secret"},
		"request_user_info": {"open_id"},
	}

	body, err := postForm(registrationEndpoint, form)
	if err != nil {
		return nil, fmt.Errorf("begin request failed: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse begin response: %w", err)
	}

	deviceCode := stringVal(raw["device_code"])
	verifyURL := stringVal(raw["verification_uri_complete"])
	userCode := stringVal(raw["user_code"])

	if deviceCode == "" || verifyURL == "" {
		return nil, fmt.Errorf("response missing device_code or verification URL: %s", string(body))
	}

	interval := intVal(raw["interval"], 5)
	expiresIn := intVal(raw["expires_in"], 300)
	if v, ok := raw["expire_in"]; ok {
		expiresIn = intVal(v, 300)
	}

	return &BeginResponse{
		DeviceCode:          deviceCode,
		VerificationURIComp: addSDKParams(verifyURL),
		UserCode:            userCode,
		Interval:            interval,
		ExpiresIn:           expiresIn,
	}, nil
}

// Poll checks the device-authorization outcome. A nil error with an empty
// PollResponse.Error means authorization succeeded.
func Poll(deviceCode string) (*PollResponse, error) {
	form := url.Values{
		"action":      {"poll"},
		"device_code": {deviceCode},
	}

	body, err := postForm(registrationEndpoint, form)
	if err != nil {
		return nil, fmt.Errorf("poll request failed: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse poll response: %w", err)
	}

	if errStr := stringVal(raw["error"]); errStr != "" {
		return &PollResponse{
			Error:            errStr,
			ErrorDescription: stringVal(raw["error_description"]),
		}, nil
	}

	return &PollResponse{
		ClientID:     stringVal(raw["client_id"]),
		ClientSecret: stringVal(raw["client_secret"]),
		UserInfo:     raw["user_info"],
	}, nil
}

// OpenID extracts the scanning user's open_id from a successful Poll's
// UserInfo, useful to default to a P2P chat without asking for a chat ID.
// Returns "" if unavailable (e.g. the user declined the user-info scope on
// an older Feishu client).
func (p *PollResponse) OpenID() string {
	m, ok := p.UserInfo.(map[string]any)
	if !ok {
		return ""
	}
	return stringVal(m["open_id"])
}

func addSDKParams(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := parsed.Query()
	q.Set("from", "sdk")
	q.Set("tp", "sdk")
	q.Set("source", "go-sdk")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func postForm(requestURL string, form url.Values) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := postClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	// Feishu returns polling states like authorization_pending/slow_down as
	// 4xx too, with an "error" field in the body — that's handled by the
	// caller (Begin/Poll) per OAuth semantics, not a request failure. Only
	// a 4xx with no "error" field is a real failure.
	if resp.StatusCode >= 400 && stringVal(raw["error"]) == "" {
		msg := stringVal(raw["error_description"])
		if msg == "" {
			msg = stringVal(raw["message"])
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}

	return json.Marshal(raw)
}

func stringVal(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func intVal(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	case string:
		var x int
		if _, err := fmt.Sscanf(n, "%d", &x); err == nil && x > 0 {
			return x
		}
	}
	return fallback
}
