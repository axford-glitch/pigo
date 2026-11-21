package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/feishuauth"
	"github.com/smallnest/pigo/internal/provider"
)

func testControl(t *testing.T) (*Control, string) {
	t.Helper()
	dir := t.TempDir()
	opt := Options{
		BindPath:   filepath.Join(dir, "feishu.json"),
		ConfigPath: filepath.Join(dir, "config.toml"),
		CredPath:   filepath.Join(dir, ".credentials.yaml"),
	}
	var polls int
	binder := newBinder(opt, func() (*feishuauth.BeginResponse, error) {
		return &feishuauth.BeginResponse{
			DeviceCode:          "dc",
			VerificationURIComp: "https://open.feishu.cn/page/launcher?user_code=ABCD",
			UserCode:            "ABCD",
			Interval:            5,
			ExpiresIn:           300,
		}, nil
	}, func(code string) (*feishuauth.PollResponse, error) {
		if code != "dc" {
			t.Errorf("device code %s", code)
		}
		polls++
		if polls == 1 {
			return &feishuauth.PollResponse{Error: "authorization_pending"}, nil
		}
		return &feishuauth.PollResponse{ClientID: "cli_app", ClientSecret: "sec_app", UserInfo: map[string]any{"open_id": "ou_owner"}}, nil
	})
	svc := &Service{
		bindPath: opt.BindPath,
		store:    fileStore{configPath: opt.ConfigPath, credPath: opt.CredPath},
		st:       Settings{Model: "keep-me"},
	}
	return &Control{Binder: binder, Svc: svc}, dir
}

func doJSON(t *testing.T, h http.Handler, method, path, body, remote string) (*httptest.ResponseRecorder, BindStatus) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st BindStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	return rec, st
}

func TestControlBindQRAndSettings(t *testing.T) {
	ctrl, dir := testControl(t)
	h := ctrl.Handler()
	rec, st := doJSON(t, h, http.MethodPost, "/v1/bind/begin", "{}", "127.0.0.1:9")
	if rec.Code != 200 || st.BindState != "pending" || st.UserCode != "ABCD" {
		t.Fatalf("begin %d %+v", rec.Code, st)
	}
	if strings.Contains(rec.Body.String(), "dc") || strings.Contains(rec.Body.String(), "sec_app") {
		t.Fatalf("begin leaked a secret: %s", rec.Body.String())
	}
	qr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/bind/qr.png", nil)
	req.RemoteAddr = "127.0.0.1:9"
	h.ServeHTTP(qr, req)
	if qr.Code != 200 || !strings.HasPrefix(qr.Body.String(), "\x89PNG") {
		t.Fatalf("qr %d %q", qr.Code, qr.Body.Bytes()[:8])
	}
	_, st = doJSON(t, h, http.MethodPost, "/v1/bind/poll", "{}", "[::1]:9")
	if st.BindState != "pending" {
		t.Fatalf("first poll %+v", st)
	}
	_, st = doJSON(t, h, http.MethodPost, "/v1/bind/poll", "{}", "127.0.0.1:9")
	if !st.Bound || st.OpenID != "ou_owner" {
		t.Fatalf("second poll %+v", st)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "feishu.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "cli_app") {
		t.Fatalf("binding file %s", raw)
	}

	rec, st = doJSON(t, h, http.MethodPost, "/v1/settings", `{"base_url":"https://api.example.com/v1","api_key":"sk-live-1234","model":"demo-model"}`, "127.0.0.1:9")
	if rec.Code != 200 || st.Model != "demo-model" || st.BaseURL != "https://api.example.com/v1" || !st.APIKeySet {
		t.Fatalf("settings %+v body %s", st, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-live-1234") {
		t.Fatalf("settings leaked the key: %s", rec.Body.String())
	}
	key, err := provider.ResolveCredentialReference(filepath.Join(dir, ".credentials.yaml"), credentialName)
	if err != nil || key != "sk-live-1234" {
		t.Fatalf("stored key (%q, %v)", key, err)
	}

	denied := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.RemoteAddr = "10.1.1.1:9"
	h.ServeHTTP(denied, req)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("lan status %d", denied.Code)
	}
}

func TestCtlStatus(t *testing.T) {
	ctrl, _ := testControl(t)
	srv := httptest.NewServer(ctrl.Handler())
	defer srv.Close()
	t.Setenv("PIGO_FEISHU_URL", srv.URL)
	var out, errOut strings.Builder
	if code := Ctl([]string{"bind-begin"}, &out, &errOut); code != 0 {
		t.Fatalf("ctl %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "ABCD") || strings.Contains(out.String(), "sec_app") {
		t.Fatalf("ctl out %s", out.String())
	}
	out.Reset()
	if code := Ctl([]string{"qr"}, &out, &errOut); code != 0 {
		t.Fatalf("qr %d %s", code, errOut.String())
	}
	if len(strings.TrimSpace(out.String())) < 20 {
		t.Fatalf("qr base64 %q", out.String())
	}
}

func TestLoopbackListenRejectsLAN(t *testing.T) {
	if err := loopbackListen("0.0.0.0:8765"); err == nil {
		t.Fatal("accepted 0.0.0.0")
	}
	if err := loopbackListen("127.0.0.1:8765"); err != nil {
		t.Fatal(err)
	}
	_ = context.Background()
}
