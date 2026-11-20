package feishuconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type textContent struct {
	Text string `json:"text"`
}

// runWebSocket keeps a long-lived SDK websocket connection alive, delivering
// message and card-action events to the registered Handler. It reconnects
// with backoff on drop; only ctx cancellation stops the loop.
func (b *Bot) runWebSocket(ctx context.Context) {
	eventHandler := b.buildDispatcher()
	runWithRetry(ctx, b.logger, "feishu websocket", func(ctx context.Context) error {
		opts := []larkws.ClientOption{
			larkws.WithEventHandler(eventHandler),
			larkws.WithLogLevel(larkcore.LogLevelError),
			larkws.WithAutoReconnect(true),
			larkws.WithOnReady(func() { b.logger.Info("feishu websocket connected") }),
			larkws.WithOnReconnecting(func() { b.logger.Warn("feishu websocket reconnecting") }),
			larkws.WithOnReconnected(func() { b.logger.Info("feishu websocket reconnected") }),
			larkws.WithOnError(func(err error) { b.logger.Error("feishu websocket error", "err", err) }),
		}
		client := larkws.NewClient(b.cfg.AppID, b.cfg.AppSecret, opts...)
		b.wsClient = client
		errCh := make(chan error, 1)
		go func() { errCh <- client.Start(ctx) }()
		select {
		case <-ctx.Done():
			client.Close()
			return nil
		case err := <-errCh:
			client.Close()
			return err
		}
	})
}

func (b *Bot) buildDispatcher() *dispatcher.EventDispatcher {
	return b.newEventDispatcher().
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			b.handleSDKMessage(ctx, event)
			return nil
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			if event == nil || event.EventReq == nil || !b.handleCardActionBytes(ctx, event.Body) {
				return cardActionToast("warning", "invalid or expired action"), nil
			}
			return cardActionToast("success", "submitted"), nil
		})
}

func (b *Bot) handleSDKMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return
	}
	eventID := ""
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		eventID = event.EventV2Base.Header.EventID
	}
	if eventID != "" && b.markSeen(eventID) {
		return
	}
	msg := event.Event.Message
	if stringPtrValue(msg.MessageType) != "text" {
		b.logger.Info("feishu message ignored", "reason", "non_text", "msg_type", stringPtrValue(msg.MessageType))
		return
	}
	var content textContent
	if err := json.Unmarshal([]byte(stringPtrValue(msg.Content)), &content); err != nil {
		b.logger.Warn("feishu message ignored", "reason", "bad_content", "err", err)
		return
	}
	chatType := ChatDM
	if stringPtrValue(msg.ChatType) == "group" || stringPtrValue(msg.ChatType) == "topic_group" {
		chatType = ChatGroup
		if b.cfg.RequireMention && len(msg.Mentions) == 0 {
			b.logger.Info("feishu message ignored", "reason", "missing_mention")
			return
		}
	}
	userID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		userID = firstNonEmpty(
			stringPtrValue(event.Event.Sender.SenderId.OpenId),
			stringPtrValue(event.Event.Sender.SenderId.UnionId),
			stringPtrValue(event.Event.Sender.SenderId.UserId),
		)
	}
	b.emit(ctx, Message{
		ChatType:  chatType,
		ChatID:    stringPtrValue(msg.ChatId),
		UserID:    userID,
		Text:      content.Text,
		MessageID: stringPtrValue(msg.MessageId),
		ThreadID:  stringPtrValue(msg.ThreadId),
		Raw:       event,
	})
}

func (b *Bot) handleCardActionBytes(ctx context.Context, raw []byte) bool {
	var payload struct {
		Header struct {
			EventID string `json:"event_id"`
		} `json:"header"`
		Event struct {
			Operator struct {
				UserID     string `json:"user_id"`
				OpenID     string `json:"open_id"`
				UnionID    string `json:"union_id"`
				OperatorID struct {
					UserID  string `json:"user_id"`
					OpenID  string `json:"open_id"`
					UnionID string `json:"union_id"`
				} `json:"operator_id"`
			} `json:"operator"`
			Context struct {
				OpenMessageID string `json:"open_message_id"`
				OpenChatID    string `json:"open_chat_id"`
			} `json:"context"`
			Action struct {
				Value map[string]string `json:"value"`
			} `json:"action"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	command := payload.Event.Action.Value["command"]
	if command == "" || payload.Event.Context.OpenChatID == "" {
		return false
	}
	if b.markSeen(payload.Header.EventID) {
		return true
	}
	chatType := cardActionChatType(payload.Event.Action.Value["chat_type"])
	operatorID := firstNonEmpty(
		payload.Event.Operator.OperatorID.UnionID,
		payload.Event.Operator.OperatorID.OpenID,
		payload.Event.Operator.OperatorID.UserID,
		payload.Event.Operator.UnionID,
		payload.Event.Operator.OpenID,
		payload.Event.Operator.UserID,
	)
	routeUserID := firstNonEmpty(payload.Event.Action.Value["user_id"], operatorID)
	b.emit(ctx, Message{
		ChatType:   chatType,
		ChatID:     payload.Event.Context.OpenChatID,
		UserID:     routeUserID,
		OperatorID: operatorID,
		Text:       command,
		MessageID:  payload.Event.Context.OpenMessageID,
	})
	return true
}

func cardActionToast(toastType, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: toastType, Content: content},
	}
}

// runWebhook serves Feishu's event-subscription webhook: URL verification
// challenge, token-authenticated message + card-action delivery.
func (b *Bot) runWebhook(ctx context.Context) {
	port := b.cfg.WebhookPort
	if port == 0 {
		port = 8080
	}

	mux := http.NewServeMux()
	mux.HandleFunc(b.cfg.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var challenge struct {
			Challenge string `json:"challenge"`
			Token     string `json:"token"`
			Type      string `json:"type"`
		}
		_ = json.Unmarshal(body, &challenge)
		if challenge.Type == "url_verification" {
			if !b.verificationTokenValid(challenge.Token) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{"challenge": challenge.Challenge}); err != nil {
				b.logger.Error("feishu challenge response error", "err", err)
			}
			return
		}

		var evt struct {
			Header struct {
				EventID string `json:"event_id"`
				Token   string `json:"token"`
			} `json:"header"`
		}
		if err := json.Unmarshal(body, &evt); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !b.verificationTokenValid(evt.Header.Token) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if !b.handleCardActionBytes(r.Context(), body) {
			b.handleWebhookEvent(r.Context(), evt.Header.EventID, body)
		}
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.Background()); err != nil && err != http.ErrServerClosed {
			b.logger.Error("feishu webhook shutdown error", "err", err)
		}
	}()

	b.logger.Info("feishu webhook listening", "port", port, "path", b.cfg.WebhookPath)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		b.logger.Error("feishu webhook server error", "err", err)
	}
}

func (b *Bot) handleWebhookEvent(ctx context.Context, eventID string, raw []byte) {
	if b.markSeen(eventID) {
		return
	}
	var evt struct {
		Header struct {
			EventType string `json:"event_type"`
		} `json:"header"`
		Event struct {
			Message struct {
				MessageID   string          `json:"message_id"`
				ChatID      string          `json:"chat_id"`
				ChatType    string          `json:"chat_type"`
				MessageType string          `json:"message_type"`
				Content     string          `json:"content"`
				ThreadID    string          `json:"thread_id"`
				Mentions    json.RawMessage `json:"mentions"`
			} `json:"message"`
			Sender struct {
				SenderID struct {
					OpenID  string `json:"open_id"`
					UnionID string `json:"union_id"`
					UserID  string `json:"user_id"`
				} `json:"sender_id"`
			} `json:"sender"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		return
	}
	if evt.Header.EventType != "im.message.receive_v1" {
		return
	}
	msg := evt.Event.Message
	if msg.MessageType != "text" {
		b.logger.Info("feishu message ignored", "reason", "non_text", "msg_type", msg.MessageType)
		return
	}
	var content textContent
	if err := json.Unmarshal([]byte(msg.Content), &content); err != nil {
		b.logger.Warn("feishu message ignored", "reason", "bad_content", "err", err)
		return
	}
	chatType := ChatDM
	if msg.ChatType == "group" || msg.ChatType == "topic_group" {
		chatType = ChatGroup
		var mentions []json.RawMessage
		_ = json.Unmarshal(msg.Mentions, &mentions)
		if b.cfg.RequireMention && len(mentions) == 0 {
			b.logger.Info("feishu message ignored", "reason", "missing_mention")
			return
		}
	}
	userID := firstNonEmpty(evt.Event.Sender.SenderID.OpenID, evt.Event.Sender.SenderID.UnionID, evt.Event.Sender.SenderID.UserID)
	b.emit(ctx, Message{
		ChatType:  chatType,
		ChatID:    msg.ChatID,
		UserID:    userID,
		Text:      content.Text,
		MessageID: msg.MessageID,
		ThreadID:  msg.ThreadID,
	})
}

func stringPtrValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return strings.TrimSpace(*ptr)
}
