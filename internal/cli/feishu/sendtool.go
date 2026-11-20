package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/run"
)

// chatIDKey carries the Feishu chat that the current turn should send files to.
type chatIDKey struct{}

func withChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, chatIDKey{}, chatID)
}

func chatIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(chatIDKey{}).(string)
	return id
}

// FileUpload sends one local file into a Feishu chat.
type FileUpload func(ctx context.Context, chatID, path string) error

// sendFileTool is how pigo itself uploads a file into the current Feishu
// chat. It is only registered for turns that arrived from Feishu.
type sendFileTool struct {
	cwd    string
	inbox  string
	chatID string
	send   FileUpload
}

func (t *sendFileTool) Name() string { return "feishu_send_file" }

func (t *sendFileTool) Description() string {
	return "把工作目录中的一个文件直接发到当前飞书对话。用户想要文件，或你刚写出用户该收到的文件时调用。文件会立刻上传。不要让用户改用 /send。"
}

func (t *sendFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "要发送的文件路径，相对工作目录或工作目录内的绝对路径。"}
  },
  "required": ["path"],
  "additionalProperties": false
}`)
}

func (t *sendFileTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *sendFileTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("path 无法解析：%w", err)
	}
	if strings.TrimSpace(body.Path) == "" {
		return agentcore.AgentToolResult{}, fmt.Errorf("path 不能为空")
	}
	if t.send == nil || strings.TrimSpace(t.chatID) == "" {
		return agentcore.AgentToolResult{}, fmt.Errorf("当前没有飞书对话可以发送")
	}
	path, err := (&Service{cwd: t.cwd, inbox: t.inbox}).resolveSendPath(body.Path)
	if err != nil {
		return agentcore.AgentToolResult{}, err
	}
	if err := t.send(ctx, t.chatID, path); err != nil {
		return agentcore.AgentToolResult{}, err
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent("已发送到飞书：" + filepath.Base(path))},
	}, nil
}

// attachSendTool gives a Feishu-backed run the feishu_send_file tool, unless
// tools are disabled or the tool policy removes it.
func attachSendTool(env *run.Env, opt Options, chatID string, send FileUpload) {
	if opt.NoTools || send == nil || strings.TrimSpace(chatID) == "" || env == nil {
		return
	}
	env.Tools = append(env.Tools, &sendFileTool{
		cwd:    opt.Cwd,
		inbox:  opt.InboxDir,
		chatID: chatID,
		send:   send,
	})
	env.Tools = run.ApplyToolPolicy(env.Tools, run.NewToolPolicy(opt.AllowedTools, opt.DisallowedTools))
	if !hasTool(env.Tools, "feishu_send_file") {
		return
	}
	env.SysPrompt += "\n\n这是飞书对话。用户需要文件时，调用 feishu_send_file 把工作目录里的文件直接发到飞书，不要让用户自己执行 /send。"
}

func hasTool(tools []agentcore.AgentTool, name string) bool {
	for _, t := range tools {
		if t != nil && t.Name() == name {
			return true
		}
	}
	return false
}
