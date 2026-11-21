package feishuconnector

import "testing"

func TestParseMessageContent(t *testing.T) {
	text, file, notice := parseMessageContent("text", `{"text":"hello"}`)
	if text != "hello" || file != nil || notice != "" {
		t.Fatalf("text = (%q, %#v, %q)", text, file, notice)
	}

	_, file, notice = parseMessageContent("image", `{"image_key":"img_1"}`)
	if notice != "" || file == nil || file.Key != "img_1" || file.Kind != "image" {
		t.Fatalf("image = %#v notice=%q", file, notice)
	}

	_, file, notice = parseMessageContent("file", `{"file_key":"file_1","file_name":"notes.txt"}`)
	if notice != "" || file == nil || file.Key != "file_1" || file.Name != "notes.txt" || file.Kind != "file" {
		t.Fatalf("file = %#v notice=%q", file, notice)
	}

	_, file, notice = parseMessageContent("audio", `{"file_key":"file_a","file_name":"a.opus"}`)
	if file == nil || file.Kind != "file" || file.Name != "a.opus" {
		t.Fatalf("audio = %#v", file)
	}

	_, _, notice = parseMessageContent("sticker", `{}`)
	if notice == "" {
		t.Fatal("sticker produced no notice")
	}

	text, file, notice = parseMessageContent("text", `{`)
	if text != "" || file != nil || notice != "" {
		t.Fatalf("bad json emitted something: %q %#v %q", text, file, notice)
	}
}
