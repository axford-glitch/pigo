package feishuconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	larknormalize "github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// SendText sends a markdown-rendered text message (falling back to plain
// text if markdown formatting fails) to a chat, or as a threaded reply if
// out.ReplyToMsgID is set via Send.
func (b *Bot) SendText(ctx context.Context, chatID, text string) (SendResult, error) {
	return b.Send(ctx, Outbound{ChatID: chatID, Text: text})
}

// Send delivers text, a card, and/or attachments. Text and attachments may
// be combined (text first, then each attachment as its own message); Card
// is sent alone.
func (b *Bot) Send(ctx context.Context, out Outbound) (SendResult, error) {
	if out.Card != nil {
		return b.sendCard(ctx, out)
	}
	var result SendResult
	text := strings.TrimSpace(out.Text)
	if text != "" {
		content, err := larknormalize.SimpleMarkdownToPost("", text, nil)
		msgType := larkim.MsgTypePost
		if err != nil {
			b.logger.Warn("format feishu markdown failed, falling back to text", "err", err)
			raw, _ := json.Marshal(textContent{Text: text})
			content, msgType = string(raw), larkim.MsgTypeText
		}
		var err2 error
		result, err2 = b.sendSDKContent(ctx, out, msgType, content)
		if err2 != nil {
			return result, err2
		}
	}
	for _, attachment := range out.Attachments {
		var err error
		result, err = b.sendAttachment(ctx, out, attachment)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// AddPendingReaction adds an "OnIt" emoji reaction to a message, useful to
// acknowledge a command is being worked on before a text reply is ready.
func (b *Bot) AddPendingReaction(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil
	}
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType("OnIt").Build()).
			Build()).
		Build()
	resp, err := b.sdkClient().Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("feishu reaction error: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reaction error: %s", feishuCodeError(resp.Code, resp.Msg))
	}
	return nil
}

func (b *Bot) sendAttachment(ctx context.Context, out Outbound, attachment Attachment) (SendResult, error) {
	path := strings.TrimSpace(attachment.Path)
	if path == "" {
		return SendResult{}, nil
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return SendResult{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return SendResult{}, err
	}
	if info.IsDir() {
		return SendResult{}, fmt.Errorf("feishu attachment is a directory: %s", abs)
	}
	if isImagePath(abs) {
		imageKey, err := b.uploadImage(ctx, abs)
		if err != nil {
			return SendResult{}, err
		}
		content, _ := json.Marshal(map[string]string{"image_key": imageKey})
		return b.sendSDKContent(ctx, out, larkim.MsgTypeImage, string(content))
	}
	filename := strings.TrimSpace(attachment.Filename)
	if filename == "" {
		filename = filepath.Base(abs)
	}
	fileKey, err := b.uploadFile(ctx, abs, filename)
	if err != nil {
		return SendResult{}, err
	}
	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	return b.sendSDKContent(ctx, out, larkim.MsgTypeFile, string(content))
}

func (b *Bot) uploadImage(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body := larkim.NewCreateImageReqBodyBuilder().
		ImageType(larkim.CreateImageImageTypeMessage).
		Image(file).
		Build()
	req := larkim.NewCreateImageReqBuilder().Body(body).Build()
	resp, err := b.sdkClient().Im.Image.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("feishu upload image error: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu upload image error: %s", feishuCodeError(resp.Code, resp.Msg))
	}
	if resp.Data == nil || stringPtrValue(resp.Data.ImageKey) == "" {
		return "", fmt.Errorf("feishu upload image error: missing image_key")
	}
	return stringPtrValue(resp.Data.ImageKey), nil
}

func (b *Bot) uploadFile(ctx context.Context, path, filename string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body := larkim.NewCreateFileReqBodyBuilder().
		FileType(fileTypeForPath(path)).
		FileName(filename).
		File(file).
		Build()
	req := larkim.NewCreateFileReqBuilder().Body(body).Build()
	resp, err := b.sdkClient().Im.File.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("feishu upload file error: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu upload file error: %s", feishuCodeError(resp.Code, resp.Msg))
	}
	if resp.Data == nil || stringPtrValue(resp.Data.FileKey) == "" {
		return "", fmt.Errorf("feishu upload file error: missing file_key")
	}
	return stringPtrValue(resp.Data.FileKey), nil
}

func fileTypeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return larkim.CreateFileFileTypePdf
	case ".doc", ".docx":
		return larkim.CreateFileFileTypeDoc
	case ".xls", ".xlsx", ".csv":
		return larkim.CreateFileFileTypeXls
	case ".ppt", ".pptx":
		return larkim.CreateFileFileTypePpt
	case ".mp4":
		return larkim.CreateFileFileTypeMp4
	default:
		return larkim.CreateFileFileTypeStream
	}
}

func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".tiff", ".tif", ".bmp", ".ico":
		return true
	default:
		return false
	}
}

func (b *Bot) sendSDKContent(ctx context.Context, out Outbound, msgType, content string) (SendResult, error) {
	client := b.sdkClient()
	if out.ReplyToMsgID != "" {
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(out.ReplyToMsgID).
			Body(larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content).Build()).
			Build()
		resp, err := client.Im.Message.Reply(ctx, req)
		if err != nil {
			return SendResult{}, err
		}
		if resp == nil {
			return SendResult{}, fmt.Errorf("feishu reply error: empty response")
		}
		if !resp.Success() {
			return SendResult{}, fmt.Errorf("feishu reply error: %s", feishuCodeError(resp.Code, resp.Msg))
		}
		if resp.Data == nil {
			return SendResult{}, nil
		}
		return SendResult{MessageID: stringPtrValue(resp.Data.MessageId)}, nil
	}

	chatID := strings.TrimSpace(out.ChatID)
	if chatID == "" {
		return SendResult{}, fmt.Errorf("feishu chat_id is empty")
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType(msgType).Content(content).Build()).
		Build()
	resp, err := client.Im.Message.Create(ctx, req)
	if err != nil {
		return SendResult{}, err
	}
	if resp == nil {
		return SendResult{}, fmt.Errorf("feishu send error: empty response")
	}
	if !resp.Success() {
		return SendResult{}, fmt.Errorf("feishu send error: %s", feishuCodeError(resp.Code, resp.Msg))
	}
	if resp.Data == nil {
		return SendResult{}, nil
	}
	return SendResult{MessageID: stringPtrValue(resp.Data.MessageId)}, nil
}

func (b *Bot) sendCard(ctx context.Context, out Outbound) (SendResult, error) {
	card := out.Card
	elements := make([]map[string]any, 0, len(card.Elements))
	for _, el := range card.Elements {
		item := map[string]any{"tag": el.Tag}
		if el.Content != "" {
			item["content"] = el.Content
		}
		if actions, ok := el.Extra["actions"]; ok && el.Tag == "action" {
			item["actions"] = actions
		} else {
			for k, v := range el.Extra {
				item[k] = v
			}
		}
		elements = append(elements, item)
	}
	cardPayload := map[string]any{
		"header": map[string]any{
			"title": map[string]string{"tag": "plain_text", "content": card.Header},
		},
		"elements": elements,
	}
	cardJSON, _ := json.Marshal(cardPayload)
	return b.sendSDKContent(ctx, out, larkim.MsgTypeInteractive, string(cardJSON))
}

func feishuCodeError(code int, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "unknown error"
	}
	if code == 0 {
		return msg
	}
	return fmt.Sprintf("%s (code %d)", msg, code)
}
