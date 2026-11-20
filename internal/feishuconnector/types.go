package feishuconnector

import "context"

// ChatType classifies where a message came from.
type ChatType string

const (
	ChatDM    ChatType = "dm"
	ChatGroup ChatType = "group"
)

// Message is an inbound event from Feishu/Lark: a text message or an
// interactive-card button click (Text carries the button's "command" value
// in that case).
type Message struct {
	ChatID    string
	ChatType  ChatType
	UserID    string
	MessageID string
	ThreadID  string
	Text      string
	// File is set for image, file, audio, and media messages. Key is the
	// Feishu resource key; Kind is the message-resource API type ("image" or
	// "file"). The bytes are not downloaded yet.
	File *InboundFile
	// Notice is a user-visible reply that must not be forwarded to the agent
	// (for example an unsupported message type).
	Notice string
	// OperatorID is set for card-action events: the authenticated actor who
	// clicked the button. UserID stays routing-only in that case.
	OperatorID string
	// Raw is the underlying SDK event, exposed as an escape hatch.
	Raw any
}

// InboundFile is a file or image attached to a Feishu message.
type InboundFile struct {
	Key  string
	Name string
	Kind string // "image" or "file"
}

// Handler is called for every inbound message/card-action. It runs on its
// own goroutine per event; do your own synchronization if needed.
type Handler func(ctx context.Context, msg Message)

// Attachment is a local file to upload and send after any text.
type Attachment struct {
	Path     string // required, local filesystem path
	Filename string // optional, defaults to filepath.Base(Path)
}

// Card is a minimal interactive card (buttons, markdown blocks) for
// approval/confirmation-style prompts.
type Card struct {
	Header   string
	Elements []CardElement
}

// CardElement is one block inside a Card. Tag follows Feishu's card schema
// ("div", "action", "hr", ...); Extra carries tag-specific fields verbatim
// (e.g. "actions" for an "action" tag).
type CardElement struct {
	Tag     string
	Content string
	Extra   map[string]any
}

// Outbound is a message to send to a chat.
type Outbound struct {
	ChatID string
	Text   string
	// ReplyToMsgID, if set, sends as a threaded reply instead of a new message.
	ReplyToMsgID string
	Attachments  []Attachment
	Card         *Card
}

// SendResult is the outcome of a successful send.
type SendResult struct {
	MessageID string
}
