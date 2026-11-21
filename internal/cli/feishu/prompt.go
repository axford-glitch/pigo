package feishu

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/cli/run"
)

// Options is the pigo run the Feishu bridge forwards chats into. Model, base
// URL, and API key are the starting values (flags and config.toml); a Feishu
// command can replace them for later messages.
type Options struct {
	Model              string
	BaseURL            string
	APIKey             string
	Protocol           string
	Provider           string
	ThinkingLevel      string
	SystemPrompt       string
	AppendSystemPrompt []string
	AllowedTools       []string
	DisallowedTools    []string
	NoTools            bool
	NoSkills           bool
	MemoryEnabled      bool
	Rebind             bool
	ConfigPath         string
	CredPath           string
	BindPath           string
	// Cwd is the directory /send may read, and the parent of the default inbox.
	Cwd string
	// InboxDir receives files downloaded from Feishu. Empty uses <Cwd>/feishu-inbox.
	InboxDir string
	// Listen is the loopback config API for LuCI. Empty disables it.
	Listen string
}

// runPrompt executes one headless pigo run and returns the final assistant
// text plus the session id carried on the first stream-json event, so the
// next Feishu message can resume the same conversation.
func runPrompt(ctx context.Context, opt Options, prompt string, st Settings, send FileUpload) (PromptResult, error) {
	env, err := run.SetupEnv(st.Model, st.BaseURL, st.Protocol, st.Provider, st.APIKey, opt.NoTools, opt.NoSkills, opt.SystemPrompt, opt.AppendSystemPrompt, opt.MemoryEnabled, run.NewToolPolicy(opt.AllowedTools, opt.DisallowedTools))
	if err != nil {
		return PromptResult{}, err
	}
	attachSendTool(&env, opt, chatIDFrom(ctx), send)
	if env.Plugins != nil {
		defer env.Plugins.Close()
	}
	if env.Memory != nil {
		defer env.Memory.Close()
	}
	mode, err := headless.ParseOutputMode("stream-json")
	if err != nil {
		return PromptResult{}, err
	}
	var stdout, stderr bytes.Buffer
	code := headless.Run(ctx, headless.RunParams{
		Mode:          mode,
		Env:           env,
		Prompt:        prompt,
		Model:         st.Model,
		APIKey:        st.APIKey,
		ThinkingLevel: opt.ThinkingLevel,
		ResumeID:      st.SessionID,
	}, &stdout, &stderr)
	sessionID, text := parseStream(stdout.String())
	if sessionID == "" {
		sessionID = st.SessionID
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprintf("pigo exited %d", code)
		}
		return PromptResult{}, fmt.Errorf("%s", msg)
	}
	return PromptResult{Text: text, SessionID: sessionID}, nil
}

// parseStream reads headless stream-json lines. sessionId comes from
// agent_start; text is the last turn_end that carried assistant text, which
// is the answer after any tool turns.
func parseStream(raw string) (sessionID, text string) {
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		switch ev["type"] {
		case "agent_start":
			if id, ok := ev["sessionId"].(string); ok && id != "" {
				sessionID = id
			}
		case "turn_end":
			if t, ok := ev["text"].(string); ok && strings.TrimSpace(t) != "" {
				text = t
			}
		}
	}
	return sessionID, text
}
