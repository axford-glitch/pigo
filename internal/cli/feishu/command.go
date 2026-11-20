package feishu

import (
	"net/url"
	"regexp"
	"strings"
)

// mentionRE matches the placeholder Feishu inserts for an @-mention in a
// text message body (@_user_1). Stripping it leaves the command or prompt.
var mentionRE = regexp.MustCompile(`@_user_\d+`)

const (
	cmdHelp   = "help"
	cmdStatus = "status"
	cmdBase   = "base"
	cmdKey    = "key"
	cmdModel  = "model"
	cmdChat   = "chat"
)

// ParseCommand classifies one inbound Feishu text. kind is help, status,
// base, key, model, or chat. arg is the remainder for the setting commands
// and the full stripped text for chat. Matching is case-insensitive for the
// ASCII forms; the argument keeps its original case (keys and URLs are
// case-sensitive).
func ParseCommand(text string) (kind, arg string) {
	text = stripMentions(text)
	if text == "" {
		return cmdHelp, ""
	}
	lower := strings.ToLower(text)
	switch lower {
	case "帮助", "/help", "help", "/?", "/h":
		return cmdHelp, ""
	case "状态", "/status", "status":
		return cmdStatus, ""
	case "/base", "/api-base", "/set-base", "设置base", "设置地址", "设置api地址", "设置base地址":
		return cmdBase, ""
	case "/key", "/apikey", "/api-key", "/set-key", "设置key", "设置apikey", "设置api key", "设置密钥":
		return cmdKey, ""
	case "/model", "设置模型":
		return cmdModel, ""
	}
	prefixes := []struct {
		prefix string
		kind   string
	}{
		{"/api-base ", cmdBase},
		{"/set-base ", cmdBase},
		{"/base ", cmdBase},
		{"设置api地址 ", cmdBase},
		{"设置base地址 ", cmdBase},
		{"设置地址 ", cmdBase},
		{"设置base ", cmdBase},
		{"/api-key ", cmdKey},
		{"/set-key ", cmdKey},
		{"/apikey ", cmdKey},
		{"/key ", cmdKey},
		{"设置api key ", cmdKey},
		{"设置apikey ", cmdKey},
		{"设置密钥 ", cmdKey},
		{"设置key ", cmdKey},
		{"/model ", cmdModel},
		{"设置模型 ", cmdModel},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p.prefix) {
			return p.kind, strings.TrimSpace(text[len(p.prefix):])
		}
	}
	return cmdChat, text
}

func stripMentions(s string) string {
	return strings.TrimSpace(mentionRE.ReplaceAllString(s, " "))
}

// normalizeBaseURL accepts an http(s) URL with a host. The returned string is
// the parsed form, so a missing scheme or host is rejected before it is saved.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errInvalidBase
	}
	return u.String(), nil
}
