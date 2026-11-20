package feishu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/feishuconnector"
	"github.com/smallnest/pigo/internal/provider"
)

func TestServiceCommandsAndForward(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	credPath := filepath.Join(dir, ".credentials.yaml")
	bindPath := filepath.Join(dir, "feishu.json")
	if err := os.WriteFile(configPath, []byte("model = \"keep-me\"\napi_key = \"sk-old\"\n[memory]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveCredential(credPath, "other", "sk-other"); err != nil {
		t.Fatal(err)
	}
	if err := SaveBinding(bindPath, Binding{AppID: "cli", AppSecret: "sec", OpenID: "ou_owner"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(bindPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("binding mode = %o, want 600", info.Mode().Perm())
	}

	var mu sync.Mutex
	var prompts []string
	var saw Settings
	svc := &Service{
		bind:     Binding{AppID: "cli", AppSecret: "sec", OpenID: "ou_owner", SessionID: "old-session"},
		bindPath: bindPath,
		st: Settings{
			Model:     "keep-me",
			SessionID: "old-session",
		},
		store: fileStore{configPath: configPath, credPath: credPath},
		runner: func(ctx context.Context, prompt string, st Settings) (PromptResult, error) {
			mu.Lock()
			prompts = append(prompts, prompt)
			saw = st
			mu.Unlock()
			return PromptResult{Text: "final answer", SessionID: "sess-1"}, nil
		},
	}
	dm := func(user, text string) feishuconnector.Message {
		return feishuconnector.Message{ChatType: feishuconnector.ChatDM, ChatID: "oc_1", UserID: user, Text: text}
	}

	if got := svc.Handle(context.Background(), feishuconnector.Message{ChatType: feishuconnector.ChatGroup, UserID: "ou_owner", Text: "/key sk-secret"}); !strings.Contains(got.Text, "私聊") {
		t.Fatalf("group reply = %q", got)
	}
	if got := svc.Handle(context.Background(), dm("ou_other", "你好")); !strings.Contains(got.Text, "没有绑定") {
		t.Fatalf("other user reply = %q", got)
	}
	if len(prompts) != 0 {
		t.Fatalf("runner called for rejected messages: %v", prompts)
	}

	baseReply := svc.Handle(context.Background(), dm("ou_owner", "设置base https://api.example.com/v1")).Text
	if !strings.Contains(baseReply, "https://api.example.com/v1") || !strings.Contains(baseReply, "openai") {
		t.Fatalf("base reply = %q", baseReply)
	}
	cfg, err := config.LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://api.example.com/v1" || cfg.Protocol != "openai" || cfg.Model != "keep-me" {
		t.Fatalf("config after base = %+v", cfg)
	}
	if cfg.Memory.Enabled == nil || *cfg.Memory.Enabled {
		t.Fatalf("memory.enabled not preserved: %+v", cfg.Memory)
	}

	keyReply := svc.Handle(context.Background(), dm("ou_owner", "/key sk-live-1234")).Text
	if strings.Contains(keyReply, "sk-live-1234") || !strings.Contains(keyReply, "1234") {
		t.Fatalf("key reply leaked or dropped the mask: %q", keyReply)
	}
	gotKey, err := provider.ResolveCredentialReference(credPath, credentialName)
	if err != nil || gotKey != "sk-live-1234" {
		t.Fatalf("stored key = (%q, %v)", gotKey, err)
	}
	other, err := provider.ResolveCredentialReference(credPath, "other")
	if err != nil || other != "sk-other" {
		t.Fatalf("other credential = (%q, %v)", other, err)
	}
	cfg, err = config.LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "" || cfg.Credential != credentialName {
		t.Fatalf("toml still holds the key or lost the reference: %+v", cfg)
	}
	if perm, err := os.Stat(credPath); err != nil || perm.Mode().Perm() != 0o600 {
		t.Fatalf("cred mode = %v %v", perm, err)
	}

	chat := svc.Handle(context.Background(), dm("ou_owner", "@_user_1 总结 README")).Text
	if chat != "final answer" {
		t.Fatalf("chat reply = %q", chat)
	}
	mu.Lock()
	if len(prompts) != 1 || prompts[0] != "总结 README" {
		t.Fatalf("prompts = %v", prompts)
	}
	if saw.BaseURL != "https://api.example.com/v1" || saw.APIKey != "sk-live-1234" || saw.Protocol != "openai" || saw.SessionID != "" {
		t.Fatalf("runner settings = %+v, want fresh session after key change", saw)
	}
	mu.Unlock()

	_ = svc.Handle(context.Background(), dm("ou_owner", "继续"))
	mu.Lock()
	second := saw.SessionID
	mu.Unlock()
	if second != "sess-1" {
		t.Fatalf("second run session = %q, want sess-1", second)
	}
	status := svc.Handle(context.Background(), dm("ou_owner", "状态")).Text
	if strings.Contains(status, "sk-live-1234") || !strings.Contains(status, "sess-1") {
		t.Fatalf("status = %q", status)
	}
}

func TestServiceBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	svc := &Service{
		bind: Binding{OpenID: "ou_owner"},
		st:   Settings{Model: "m"},
		runner: func(ctx context.Context, prompt string, st Settings) (PromptResult, error) {
			close(started)
			<-release
			return PromptResult{Text: "done"}, nil
		},
	}
	msg := feishuconnector.Message{ChatType: feishuconnector.ChatDM, UserID: "ou_owner", Text: "hello"}
	done := make(chan string, 1)
	go func() { done <- svc.Handle(context.Background(), msg).Text }()
	<-started
	busy := svc.Handle(context.Background(), msg).Text
	if !strings.Contains(busy, "上一条") {
		t.Fatalf("busy reply = %q", busy)
	}
	close(release)
	if got := <-done; got != "done" {
		t.Fatalf("first reply = %q", got)
	}
}

func TestParseStream(t *testing.T) {
	raw := "{\"type\":\"agent_start\",\"sessionId\":\"sess-9\"}\n{\"type\":\"turn_end\",\"text\":\"working\",\"toolCalls\":[\"bash\"]}\n{\"type\":\"turn_end\",\"text\":\"final answer\"}\n"
	id, text := parseStream(raw)
	if id != "sess-9" || text != "final answer" {
		t.Fatalf("parseStream = (%q, %q)", id, text)
	}
}
