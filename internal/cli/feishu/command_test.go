package feishu

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		in, kind, arg string
	}{
		{"帮助", cmdHelp, ""},
		{"/help", cmdHelp, ""},
		{"状态", cmdStatus, ""},
		{"/STATUS", cmdStatus, ""},
		{"/base https://api.example.com/v1", cmdBase, "https://api.example.com/v1"},
		{"设置地址 https://API.Example.com/V1", cmdBase, "https://API.Example.com/V1"},
		{"设置api地址 http://127.0.0.1:11434/v1", cmdBase, "http://127.0.0.1:11434/v1"},
		{"/key sk-ABCDzzzz", cmdKey, "sk-ABCDzzzz"},
		{"设置密钥 sk-ABCDzzzz", cmdKey, "sk-ABCDzzzz"},
		{"设置api key sk-ABCDzzzz", cmdKey, "sk-ABCDzzzz"},
		{"/model gpt-4o-mini", cmdModel, "gpt-4o-mini"},
		{"设置模型 deepseek-chat", cmdModel, "deepseek-chat"},
		{"@_user_1 总结 README", cmdChat, "总结 README"},
		{"  ", cmdHelp, ""},
		{"/base", cmdBase, ""},
		{"设置key", cmdKey, ""},
	}
	for _, tt := range tests {
		kind, arg := ParseCommand(tt.in)
		if kind != tt.kind || arg != tt.arg {
			t.Errorf("ParseCommand(%q) = (%q, %q), want (%q, %q)", tt.in, kind, arg, tt.kind, tt.arg)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := normalizeBaseURL("https://api.example.com/v1")
	if err != nil || got != "https://api.example.com/v1" {
		t.Fatalf("normalize = (%q, %v)", got, err)
	}
	if _, err := normalizeBaseURL("ftp://api.example.com"); err == nil {
		t.Fatal("ftp URL accepted")
	}
	if _, err := normalizeBaseURL("not a url"); err == nil {
		t.Fatal("bare text accepted")
	}
}
