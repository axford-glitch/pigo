package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/smallnest/pigo/internal/feishuconnector"
)

// errInvalidBase is returned by normalizeBaseURL and matched by setBase so
// the Feishu reply can explain the expected shape without a parse dump.
var errInvalidBase = errors.New("invalid base url")

// Settings is the model endpoint the next forwarded chat will use.
type Settings struct {
	Model     string
	BaseURL   string
	APIKey    string
	Protocol  string
	Provider  string
	SessionID string
}

// PromptResult is one forwarded pigo run.
type PromptResult struct {
	Text      string
	SessionID string
}

// PromptRunner executes one prompt against pigo. Tests substitute it; the
// real runner is runPrompt.
type PromptRunner func(ctx context.Context, prompt string, st Settings) (PromptResult, error)

// Service turns inbound Feishu messages into settings changes or a pigo run.
// Only a direct message from the bound open_id is acted on.
type Service struct {
	bind     Binding
	st       Settings
	runner   PromptRunner
	store    fileStore
	bindPath string

	mu   sync.Mutex
	busy atomic.Bool
}

func (s *Service) snapshot() (Binding, Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bind, s.st
}

func (s *Service) allow(userID string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID = strings.TrimSpace(userID)
	if s.bind.OpenID == "" {
		if userID == "" {
			return false, "无法识别飞书用户，绑定没有完成。"
		}
		s.bind.OpenID = userID
		if err := SaveBinding(s.bindPath, s.bind); err != nil {
			s.bind.OpenID = ""
			return false, "保存绑定用户失败：" + err.Error()
		}
		return true, ""
	}
	if userID != s.bind.OpenID {
		return false, "这个飞书账号没有绑定本机 pigo。"
	}
	return true, ""
}

// Handle replies to one inbound message. Group chats are refused so a key or
// a workspace answer is not posted where other people can read it.
func (s *Service) Handle(ctx context.Context, msg feishuconnector.Message) string {
	if msg.ChatType != feishuconnector.ChatDM {
		return "请私聊我。群消息不会执行，避免把 API key 和仓库内容发到群里。"
	}
	ok, reply := s.allow(msg.UserID)
	if !ok {
		return reply
	}
	kind, arg := ParseCommand(msg.Text)
	switch kind {
	case cmdHelp:
		return helpText
	case cmdStatus:
		return s.status()
	case cmdBase:
		return s.setBase(arg)
	case cmdKey:
		return s.setKey(arg)
	case cmdModel:
		return s.setModel(arg)
	default:
		return s.forward(ctx, arg)
	}
}

func (s *Service) status() string {
	_, st := s.snapshot()
	base := st.BaseURL
	if base == "" {
		base = "（未设置，使用模型默认地址）"
	}
	key := "未设置"
	if st.APIKey != "" {
		key = "已设置（" + maskKey(st.APIKey) + "）"
	}
	session := "新会话"
	if st.SessionID != "" {
		session = "续聊 " + st.SessionID
	}
	return fmt.Sprintf("模型：%s\nAPI base：%s\n协议：%s\nAPI key：%s\n会话：%s",
		emptyAs(st.Model, "（未设置）"), base, emptyAs(st.Protocol, "（自动）"), key, session)
}

func (s *Service) setBase(arg string) string {
	base, err := normalizeBaseURL(arg)
	if err != nil || arg == "" {
		return "用法：/base https://api.example.com/v1 或 设置base https://api.example.com/v1"
	}
	s.mu.Lock()
	protocol := s.st.Protocol
	if protocol == "" {
		protocol = "openai"
	}
	s.mu.Unlock()
	if err := s.store.SaveBase(base, protocol); err != nil {
		return "保存 API base 失败：" + err.Error()
	}
	s.mu.Lock()
	s.st.BaseURL = base
	s.st.Protocol = protocol
	s.st.SessionID = ""
	s.bind.SessionID = ""
	bind := s.bind
	path := s.bindPath
	s.mu.Unlock()
	return noteBind(SaveBinding(path, bind), "API base 已设置为 "+base+"。协议是 "+protocol+"。下一条对话会用这个地址，并且从新会话开始。")
}

func (s *Service) setKey(arg string) string {
	key := strings.TrimSpace(arg)
	if key == "" {
		return "用法：/key <api-key> 或 设置key <api-key>。密钥只写在本机凭据文件里。"
	}
	if err := s.store.SaveKey(key); err != nil {
		return "保存 API key 失败：" + err.Error()
	}
	s.mu.Lock()
	s.st.APIKey = key
	s.st.SessionID = ""
	s.bind.SessionID = ""
	bind := s.bind
	path := s.bindPath
	s.mu.Unlock()
	saved := "API key 已保存（" + maskKey(key) + "）。完整密钥不会发回飞书，下一条对话会用它。"
	return noteBind(SaveBinding(path, bind), saved)
}

func (s *Service) setModel(arg string) string {
	model := strings.TrimSpace(arg)
	if model == "" {
		return "用法：/model <模型 id> 或 设置模型 <模型 id>"
	}
	if err := s.store.SaveModel(model); err != nil {
		return "保存模型失败：" + err.Error()
	}
	s.mu.Lock()
	s.st.Model = model
	s.st.SessionID = ""
	s.bind.SessionID = ""
	bind := s.bind
	path := s.bindPath
	s.mu.Unlock()
	return noteBind(SaveBinding(path, bind), "模型已设置为 "+model+"。下一条对话从新会话开始。")
}

func (s *Service) forward(ctx context.Context, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return helpText
	}
	if s.runner == nil {
		return "pigo 还没有准备好转发对话。"
	}
	if !s.busy.CompareAndSwap(false, true) {
		return "上一条还在处理，请稍后再发。"
	}
	defer s.busy.Store(false)

	_, st := s.snapshot()
	res, err := s.runner(ctx, prompt, st)
	if err != nil {
		return "pigo 执行失败：" + err.Error()
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		text = "（模型没有返回文本）"
	}
	if res.SessionID != "" {
		s.mu.Lock()
		s.st.SessionID = res.SessionID
		s.bind.SessionID = res.SessionID
		bind := s.bind
		path := s.bindPath
		s.mu.Unlock()
		if err := SaveBinding(path, bind); err != nil {
			text += "\n（会话没能写入绑定文件：" + err.Error() + "）"
		}
	}
	return truncateRunes(text, 8000)
}

func noteBind(err error, reply string) string {
	if err != nil {
		return reply + "\n（绑定文件没能更新：" + err.Error() + "）"
	}
	return reply
}

func maskKey(key string) string {
	if len(key) <= 4 {
		return "…"
	}
	return "…" + key[len(key)-4:]
}

func emptyAs(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…(已截断)"
}

const helpText = `飞书命令（请在私聊里发）：
/base <url> 或 设置base <url> — 设置模型 API base 地址
/key <key> 或 设置key <key> — 设置 API key（只保存在本机）
/model <id> 或 设置模型 <id> — 设置模型 id
状态 — 查看当前设置（不显示完整 key）
其他文字 — 转发给 pigo，并把回答回传到这里`
