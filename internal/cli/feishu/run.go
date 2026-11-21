// Package feishu binds a Feishu bot with a QR code and accepts commands from
// the scanning user: set the model API base URL, set the API key, or forward
// the rest of the conversation to pigo and send the answer back.
package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/feishuauth"
	"github.com/smallnest/pigo/internal/feishuconnector"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/remotecontrol"
	"golang.org/x/term"
)

type bridgeDeps struct {
	begin    func() (*feishuauth.BeginResponse, error)
	poll     func(deviceCode string) (*feishuauth.PollResponse, error)
	render   func(content string) (string, error)
	wait     func(ctx context.Context, d time.Duration) error
	startBot func(ctx context.Context, appID, secret string, svc *Service) (stop func(), err error)
	runner   PromptRunner
}

// Run shows a Feishu QR code when this machine is not bound yet, then serves
// commands until ctx is cancelled (or the process receives SIGINT/SIGTERM).
func Run(ctx context.Context, opt Options, out, errOut io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	return serve(ctx, opt, out, errOut, realDeps())
}

func realDeps() bridgeDeps {
	return bridgeDeps{
		begin:  feishuauth.Begin,
		poll:   feishuauth.Poll,
		render: remotecontrol.Render,
		wait:   waitCtx,
		startBot: func(ctx context.Context, appID, secret string, svc *Service) (func(), error) {
			bot, err := feishuconnector.New(feishuconnector.Config{
				AppID:          appID,
				AppSecret:      secret,
				Mode:           "websocket",
				RequireMention: true,
			})
			if err != nil {
				return nil, err
			}
			svc.fetch = func(ctx context.Context, messageID, fileKey, kind string) ([]byte, string, error) {
				return bot.DownloadResource(ctx, messageID, fileKey, kind)
			}
			svc.sendFile = func(ctx context.Context, chatID, path string) error {
				_, err := bot.Send(ctx, feishuconnector.Outbound{
					ChatID:      chatID,
					Attachments: []feishuconnector.Attachment{{Path: path}},
				})
				return err
			}
			bot.OnMessage(func(ctx context.Context, msg feishuconnector.Message) {
				reply := svc.Handle(ctx, msg)
				if strings.TrimSpace(reply.Text) != "" {
					if _, err := bot.SendText(ctx, msg.ChatID, reply.Text); err != nil {
						fmt.Fprintf(os.Stderr, "pigo: feishu send: %v\n", err)
					}
				}
				for _, path := range reply.Files {
					if _, err := bot.Send(ctx, feishuconnector.Outbound{
						ChatID:      msg.ChatID,
						Attachments: []feishuconnector.Attachment{{Path: path}},
					}); err != nil {
						fmt.Fprintf(os.Stderr, "pigo: feishu file: %v\n", err)
					}
				}
			})
			if err := bot.Start(ctx); err != nil {
				return nil, err
			}
			return func() { _ = bot.Stop() }, nil
		},
	}
}

func waitCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = 5 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func serve(ctx context.Context, opt Options, out, errOut io.Writer, d bridgeDeps) int {
	ctx, stopSig := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSig()
	if opt.BindPath == "" {
		opt.BindPath = defaultBindPath()
	}
	if opt.ConfigPath == "" {
		opt.ConfigPath = config.FileConfigPath()
	}
	if opt.CredPath == "" {
		opt.CredPath = provider.CredentialFilePath()
	}
	binder := newBinder(opt, d.begin, d.poll)
	existing := binder.currentBinding()
	svc := &Service{
		bind:     existing,
		bindPath: opt.BindPath,
		st: Settings{
			Model:     opt.Model,
			BaseURL:   opt.BaseURL,
			APIKey:    opt.APIKey,
			Protocol:  opt.Protocol,
			Provider:  opt.Provider,
			SessionID: existing.SessionID,
		},
		store: fileStore{configPath: opt.ConfigPath, credPath: opt.CredPath},
		cwd:   opt.Cwd,
		inbox: opt.InboxDir,
	}
	if d.runner != nil {
		svc.runner = d.runner
	} else {
		svc.runner = func(ctx context.Context, prompt string, st Settings) (PromptResult, error) {
			return runPrompt(ctx, opt, prompt, st, svc.sendFile)
		}
	}
	slot := &botSlot{}
	binder.onBound = func(b Binding) {
		svc.setBinding(b)
		if opt.Listen != "" && !stdoutTerminal(out) {
			if err := slot.ensure(ctx, b, svc, d.startBot); err != nil {
				fmt.Fprintf(errOut, "pigo: feishu: %v\n", err)
			}
		}
	}
	if opt.Listen != "" {
		ctrl := &Control{Binder: binder, Svc: svc, Listen: opt.Listen}
		go func() {
			if err := ctrl.Serve(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(errOut, "pigo: 飞书配置接口: %v\n", err)
			}
		}()
		fmt.Fprintf(out, "飞书配置接口：http://%s\n", opt.Listen)
	}
	// A headless OpenWrt service waits for LuCI to scan. A terminal still blocks
	// on the QR the way pigo --feishu always has.
	if opt.Listen != "" && !stdoutTerminal(out) {
		if existing := binder.currentBinding(); existing.usable() && !opt.Rebind {
			fmt.Fprintln(out, "已使用保存的飞书绑定。LuCI 可以重新扫码。")
			if err := slot.ensure(ctx, existing, svc, d.startBot); err != nil {
				fmt.Fprintf(errOut, "pigo: feishu: %v\n", err)
				return 1
			}
		} else {
			fmt.Fprintln(out, "尚未绑定飞书。在 LuCI 页面扫码。")
		}
		<-ctx.Done()
		slot.shutdown()
		return 0
	}
	bind, err := acquireBinding(ctx, opt, out, d, binder)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	svc.setBinding(bind)
	if err := slot.ensure(ctx, bind, svc, d.startBot); err != nil {
		fmt.Fprintf(errOut, "pigo: feishu: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "飞书已连接。私聊可以设置 API base、API key，收发文件，或把对话转发给 pigo。Ctrl+C 退出。")
	<-ctx.Done()
	slot.shutdown()
	return 0
}

type botSlot struct {
	mu     sync.Mutex
	stopFn func()
	appID  string
}

func (s *botSlot) ensure(ctx context.Context, b Binding, svc *Service, start func(context.Context, string, string, *Service) (func(), error)) error {
	if !b.usable() || start == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopFn != nil && s.appID == b.AppID {
		return nil
	}
	if s.stopFn != nil {
		s.stopFn()
		s.stopFn = nil
		s.appID = ""
	}
	stop, err := start(ctx, b.AppID, b.AppSecret, svc)
	if err != nil {
		return err
	}
	s.stopFn = stop
	s.appID = b.AppID
	return nil
}

func (s *botSlot) shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopFn != nil {
		s.stopFn()
		s.stopFn = nil
	}
}

func stdoutTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func acquireBinding(ctx context.Context, opt Options, out io.Writer, d bridgeDeps, binder *Binder) (Binding, error) {
	if !opt.Rebind {
		existing := binder.currentBinding()
		if existing.usable() {
			fmt.Fprintln(out, "已使用保存的飞书绑定。加 --feishu-rebind 可以重新扫码。")
			return existing, nil
		}
	}
	st, err := binder.Begin()
	if err != nil {
		return Binding{}, err
	}
	fmt.Fprintln(out, "用飞书扫描二维码，把这台机器上的 pigo 绑到你的飞书账号：")
	url := binder.verificationURL()
	if qr, err := d.render(url); err == nil && qr != "" {
		fmt.Fprint(out, qr)
		if qr[len(qr)-1] != '\n' {
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintf(out, "或打开：%s\n", url)
	if st.UserCode != "" {
		fmt.Fprintf(out, "校验码：%s\n", st.UserCode)
	}
	for {
		if err := ctx.Err(); err != nil {
			return Binding{}, err
		}
		st, err = binder.Poll(ctx)
		if err != nil {
			return Binding{}, err
		}
		switch st.BindState {
		case "bound":
			fmt.Fprintln(out, "飞书绑定成功。在飞书里私聊这个机器人。")
			return binder.currentBinding(), nil
		case "expired":
			return Binding{}, errors.New("飞书绑定二维码已过期，请重新运行 pigo --feishu")
		case "error":
			msg := st.Message
			if msg == "" {
				msg = "飞书绑定失败"
			}
			return Binding{}, errors.New(msg)
		default:
			interval := st.Interval
			if interval <= 0 {
				interval = 5
			}
			if err := d.wait(ctx, time.Duration(interval)*time.Second); err != nil {
				return Binding{}, err
			}
		}
	}
}
