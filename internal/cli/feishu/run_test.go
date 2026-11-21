package feishu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/feishuauth"
	"github.com/smallnest/pigo/internal/feishuconnector"
)

func TestRunQRBindThenForward(t *testing.T) {
	dir := t.TempDir()
	var polls int
	var mu sync.Mutex
	var on *Service
	started := make(chan struct{})
	deps := bridgeDeps{
		begin: func() (*feishuauth.BeginResponse, error) {
			return &feishuauth.BeginResponse{
				DeviceCode:          "dc",
				VerificationURIComp: "https://accounts.feishu.cn/oauth/verify?user_code=ABCD",
				UserCode:            "ABCD",
				Interval:            5,
				ExpiresIn:           300,
			}, nil
		},
		poll: func(deviceCode string) (*feishuauth.PollResponse, error) {
			if deviceCode != "dc" {
				t.Errorf("device code %q", deviceCode)
			}
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n == 1 {
				return &feishuauth.PollResponse{Error: "authorization_pending"}, nil
			}
			return &feishuauth.PollResponse{
				ClientID:     "cli_app",
				ClientSecret: "sec_app",
				UserInfo:     map[string]any{"open_id": "ou_owner"},
			}, nil
		},
		render: func(content string) (string, error) { return "QR:" + content + "\n", nil },
		wait:   func(ctx context.Context, d time.Duration) error { return nil },
		startBot: func(ctx context.Context, appID, secret string, handle *Service) (func(), error) {
			if appID != "cli_app" || secret != "sec_app" {
				t.Errorf("bot credentials = %s %s", appID, secret)
			}
			mu.Lock()
			on = handle
			mu.Unlock()
			close(started)
			return func() {}, nil
		},
		runner: func(ctx context.Context, prompt string, st Settings) (PromptResult, error) {
			if st.APIKey != "sk-test-9999" || st.BaseURL != "https://api.example.com/v1" {
				t.Errorf("forward settings = %+v", st)
			}
			return PromptResult{Text: "pong " + prompt, SessionID: "sess-new"}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opt := Options{
		Model:      "m",
		ConfigPath: filepath.Join(dir, "config.toml"),
		CredPath:   filepath.Join(dir, ".credentials.yaml"),
		BindPath:   filepath.Join(dir, "feishu.json"),
	}
	var out, errOut strings.Builder
	codeCh := make(chan int, 1)
	go func() { codeCh <- serve(ctx, opt, &out, &errOut, deps) }()
	<-started

	mu.Lock()
	handle := on
	mu.Unlock()
	base := handle.Handle(context.Background(), feishuconnector.Message{ChatType: feishuconnector.ChatDM, UserID: "ou_owner", Text: "/base https://api.example.com/v1"}).Text
	key := handle.Handle(context.Background(), feishuconnector.Message{ChatType: feishuconnector.ChatDM, UserID: "ou_owner", Text: "设置key sk-test-9999"}).Text
	chat := handle.Handle(context.Background(), feishuconnector.Message{ChatType: feishuconnector.ChatDM, UserID: "ou_owner", Text: "你好"}).Text
	cancel()
	if code := <-codeCh; code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut.String())
	}
	printed := out.String()
	if !strings.Contains(printed, "https://accounts.feishu.cn/oauth/verify?user_code=ABCD") || !strings.Contains(printed, "QR:") || !strings.Contains(printed, "ABCD") || !strings.Contains(printed, "绑定成功") {
		t.Fatalf("stdout missing QR bind info:\n%s", printed)
	}
	if !strings.Contains(base, "https://api.example.com/v1") {
		t.Fatalf("base reply = %q", base)
	}
	if strings.Contains(key, "sk-test-9999") {
		t.Fatalf("key reply leaked the secret: %q", key)
	}
	if chat != "pong 你好" {
		t.Fatalf("chat reply = %q", chat)
	}
	bind, err := LoadBinding(opt.BindPath)
	if err != nil {
		t.Fatal(err)
	}
	if bind.AppID != "cli_app" || bind.AppSecret != "sec_app" || bind.OpenID != "ou_owner" || bind.SessionID != "sess-new" {
		t.Fatalf("binding = %+v", bind)
	}
	if info, err := os.Stat(opt.BindPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("binding mode = %v %v", info, err)
	}
}

func TestRunUsesSavedBinding(t *testing.T) {
	dir := t.TempDir()
	bindPath := filepath.Join(dir, "feishu.json")
	if err := SaveBinding(bindPath, Binding{AppID: "saved", AppSecret: "saved-sec", OpenID: "ou_owner", SessionID: "sess-old"}); err != nil {
		t.Fatal(err)
	}
	beginCalled := false
	started := make(chan struct{})
	deps := bridgeDeps{
		begin: func() (*feishuauth.BeginResponse, error) {
			beginCalled = true
			return nil, os.ErrClosed
		},
		poll:   func(string) (*feishuauth.PollResponse, error) { return nil, os.ErrClosed },
		render: func(string) (string, error) { return "", nil },
		wait:   func(context.Context, time.Duration) error { return nil },
		startBot: func(ctx context.Context, appID, secret string, on *Service) (func(), error) {
			if appID != "saved" || secret != "saved-sec" {
				t.Errorf("credentials = %s %s", appID, secret)
			}
			close(started)
			return func() {}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut strings.Builder
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- serve(ctx, Options{BindPath: bindPath, ConfigPath: filepath.Join(dir, "c.toml"), CredPath: filepath.Join(dir, "k.yaml")}, &out, &errOut, deps)
	}()
	<-started
	cancel()
	if code := <-codeCh; code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut.String())
	}
	if beginCalled {
		t.Fatal("saved binding started a new QR flow")
	}
	if !strings.Contains(out.String(), "已使用保存的飞书绑定") {
		t.Fatalf("stdout = %q", out.String())
	}
}
