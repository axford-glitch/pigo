package feishu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/feishuconnector"
)

func TestReceiveFileStaysInInbox(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "feishu-inbox")
	var gotKey, gotKind, gotMsg string
	svc := &Service{
		bind:  Binding{OpenID: "ou_owner"},
		inbox: inbox,
		fetch: func(ctx context.Context, messageID, fileKey, kind string) ([]byte, string, error) {
			gotMsg, gotKey, gotKind = messageID, fileKey, kind
			return []byte("hello file"), "ignored-header.txt", nil
		},
	}
	reply := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType:  feishuconnector.ChatDM,
		UserID:    "ou_owner",
		MessageID: "om_1",
		File:      &feishuconnector.InboundFile{Key: "file_1", Name: "../../notes.txt", Kind: "file"},
	})
	if gotMsg != "om_1" || gotKey != "file_1" || gotKind != "file" {
		t.Fatalf("fetch args = %s %s %s", gotMsg, gotKey, gotKind)
	}
	saved := filepath.Join(inbox, "notes.txt")
	body, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello file" {
		t.Fatalf("body = %q", body)
	}
	info, err := os.Stat(saved)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if !strings.Contains(reply.Text, saved) || len(reply.Files) != 0 {
		t.Fatalf("reply = %+v", reply)
	}
	outside := filepath.Join(filepath.Dir(inbox), "notes.txt")
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote %s", outside)
	}

	again := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatDM,
		UserID:   "ou_owner",
		File:     &feishuconnector.InboundFile{Key: "file_2", Name: "notes.txt", Kind: "file"},
	})
	second := filepath.Join(inbox, "notes-2.txt")
	if _, err := os.Stat(second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again.Text, second) {
		t.Fatalf("second reply = %q", again.Text)
	}
}

func TestReceiveImageUsesHeaderName(t *testing.T) {
	inbox := t.TempDir()
	svc := &Service{
		bind:  Binding{OpenID: "ou_owner"},
		inbox: inbox,
		fetch: func(ctx context.Context, messageID, fileKey, kind string) ([]byte, string, error) {
			if kind != "image" {
				t.Errorf("kind = %s", kind)
			}
			return []byte("png"), "shot.png", nil
		},
	}
	reply := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatDM,
		UserID:   "ou_owner",
		File:     &feishuconnector.InboundFile{Key: "img_1", Kind: "image"},
	})
	if _, err := os.Stat(filepath.Join(inbox, "shot.png")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Text, "shot.png") {
		t.Fatalf("reply = %q", reply.Text)
	}
}

func TestSendLocalFile(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	svc := &Service{bind: Binding{OpenID: "ou_owner"}, cwd: cwd}
	reply := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatDM,
		UserID:   "ou_owner",
		Text:     "发送文件 notes.txt",
	})
	if len(reply.Files) != 1 || reply.Files[0] != filepath.Join(cwd, "notes.txt") {
		t.Fatalf("files = %#v text=%q", reply.Files, reply.Text)
	}
	if !strings.Contains(reply.Text, "notes.txt") {
		t.Fatalf("text = %q", reply.Text)
	}

	denied := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatDM,
		UserID:   "ou_owner",
		Text:     "/send " + filepath.Join(outside, "secret.txt"),
	})
	if len(denied.Files) != 0 || !strings.Contains(denied.Text, "只能发送") {
		t.Fatalf("outside file was accepted: %+v", denied)
	}
	linked := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatDM,
		UserID:   "ou_owner",
		Text:     "/send link.txt",
	})
	if len(linked.Files) != 0 || !strings.Contains(linked.Text, "只能发送") {
		t.Fatalf("symlink escape was accepted: %+v", linked)
	}
}

func TestGroupFileIsNotDownloaded(t *testing.T) {
	called := false
	svc := &Service{
		bind: Binding{OpenID: "ou_owner"},
		fetch: func(ctx context.Context, messageID, fileKey, kind string) ([]byte, string, error) {
			called = true
			return nil, "", nil
		},
	}
	reply := svc.Handle(context.Background(), feishuconnector.Message{
		ChatType: feishuconnector.ChatGroup,
		UserID:   "ou_owner",
		File:     &feishuconnector.InboundFile{Key: "file_1", Name: "a.txt", Kind: "file"},
	})
	if called || !strings.Contains(reply.Text, "私聊") {
		t.Fatalf("group file reply = %+v called=%v", reply, called)
	}
}
