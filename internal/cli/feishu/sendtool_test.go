package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/run"
	"github.com/smallnest/pigo/internal/feishuconnector"
)

func contentText(content agentcore.ContentList) (string, error) {
	if len(content) != 1 {
		return "", fmt.Errorf("%d blocks", len(content))
	}
	text, ok := content[0].(agentcore.TextContent)
	if !ok {
		return "", fmt.Errorf("content type %T", content[0])
	}
	return text.Text, nil
}

func TestSendFileToolUploadsInsideWorkdir(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotChat, gotPath string
	tool := &sendFileTool{
		cwd:    cwd,
		chatID: "oc_1",
		send: func(ctx context.Context, chatID, path string) error {
			gotChat, gotPath = chatID, path
			return nil
		},
	}
	res, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"notes.txt"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotChat != "oc_1" || gotPath != filepath.Join(cwd, "notes.txt") {
		t.Fatalf("upload = %s %s", gotChat, gotPath)
	}
	got, err := contentText(res.Content)
	if err != nil || !strings.Contains(got, "notes.txt") {
		t.Fatalf("result = %q %v", got, err)
	}
}

func TestSendFileToolRejectsEscape(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	called := false
	tool := &sendFileTool{
		cwd:    cwd,
		chatID: "oc_1",
		send: func(ctx context.Context, chatID, path string) error {
			called = true
			return nil
		},
	}
	if _, err := tool.Execute(context.Background(), "c", json.RawMessage(`{"path":"`+filepath.Join(outside, "secret.txt")+`"}`), nil); err == nil {
		t.Fatal("outside path was sent")
	}
	if _, err := tool.Execute(context.Background(), "c", json.RawMessage(`{"path":"link.txt"}`), nil); err == nil {
		t.Fatal("symlink was sent")
	}
	if called {
		t.Fatal("upload was called")
	}
}

func TestAttachSendToolHonorsDeny(t *testing.T) {
	env := &run.Env{}
	opt := Options{DisallowedTools: []string{"feishu_send_file"}}
	attachSendTool(env, opt, "oc_1", func(ctx context.Context, chatID, path string) error { return nil })
	if hasTool(env.Tools, "feishu_send_file") {
		t.Fatal("denied tool stayed registered")
	}
	if strings.Contains(env.SysPrompt, "feishu_send_file") {
		t.Fatal("prompt mentioned a removed tool")
	}
	opt = Options{}
	attachSendTool(env, opt, "oc_1", func(ctx context.Context, chatID, path string) error { return nil })
	if !hasTool(env.Tools, "feishu_send_file") || !strings.Contains(env.SysPrompt, "feishu_send_file") {
		t.Fatalf("tool missing: tools=%v prompt=%q", env.Tools, env.SysPrompt)
	}
}

func TestForwardCarriesChatID(t *testing.T) {
	var got string
	svc := &Service{
		bind: Binding{OpenID: "ou_owner"},
		runner: func(ctx context.Context, prompt string, st Settings) (PromptResult, error) {
			got = chatIDFrom(ctx)
			return PromptResult{Text: "ok"}, nil
		},
	}
	reply := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatDM,
		ChatID:   "oc_chat",
		UserID:   "ou_owner",
		Text:     "把 README 发给我",
	})
	if got != "oc_chat" || reply.Text != "ok" {
		t.Fatalf("chat=%q reply=%+v", got, reply)
	}
}
