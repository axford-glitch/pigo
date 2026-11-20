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
	"syscall"
	"time"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/feishuauth"
	"github.com/smallnest/pigo/internal/feishuconnector"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/remotecontrol"
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
	bind, err := acquireBinding(ctx, opt, out, d)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	svc := &Service{
		bind:     bind,
		bindPath: opt.BindPath,
		st: Settings{
			Model:     opt.Model,
			BaseURL:   opt.BaseURL,
			APIKey:    opt.APIKey,
			Protocol:  opt.Protocol,
			Provider:  opt.Provider,
			SessionID: bind.SessionID,
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
	stopBot, err := d.startBot(ctx, bind.AppID, bind.AppSecret, svc)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: feishu: %v\n", err)
		return 1
	}
	defer stopBot()
	fmt.Fprintln(out, "飞书已连接。私聊可以设置 API base、API key，收发文件，或把对话转发给 pigo。Ctrl+C 退出。")
	<-ctx.Done()
	return 0
}

func acquireBinding(ctx context.Context, opt Options, out io.Writer, d bridgeDeps) (Binding, error) {
	if !opt.Rebind {
		existing, err := LoadBinding(opt.BindPath)
		if err != nil {
			return Binding{}, err
		}
		if existing.usable() {
			fmt.Fprintln(out, "已使用保存的飞书绑定。加 --feishu-rebind 可以重新扫码。")
			return existing, nil
		}
	}
	begin, err := d.begin()
	if err != nil {
		return Binding{}, err
	}
	fmt.Fprintln(out, "用飞书扫描二维码，把这台机器上的 pigo 绑到你的飞书账号：")
	if qr, err := d.render(begin.VerificationURIComp); err == nil && qr != "" {
		fmt.Fprint(out, qr)
		if qr[len(qr)-1] != '\n' {
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintf(out, "或打开：%s\n", begin.VerificationURIComp)
	if begin.UserCode != "" {
		fmt.Fprintf(out, "校验码：%s\n", begin.UserCode)
	}
	interval := begin.Interval
	if interval <= 0 {
		interval = 5
	}
	expires := begin.ExpiresIn
	if expires <= 0 {
		expires = 300
	}
	deadline := time.Now().Add(time.Duration(expires) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return Binding{}, err
		}
		if time.Now().After(deadline) {
			return Binding{}, errors.New("飞书绑定二维码已过期，请重新运行 pigo --feishu")
		}
		poll, err := d.poll(begin.DeviceCode)
		if err != nil {
			return Binding{}, err
		}
		switch poll.Error {
		case "":
			if poll.ClientID == "" || poll.ClientSecret == "" {
				return Binding{}, errors.New("飞书没有返回 app id 和 app secret")
			}
			b := Binding{AppID: poll.ClientID, AppSecret: poll.ClientSecret, OpenID: poll.OpenID()}
			if err := SaveBinding(opt.BindPath, b); err != nil {
				return Binding{}, err
			}
			fmt.Fprintln(out, "飞书绑定成功。在飞书里私聊这个机器人。")
			return b, nil
		case "authorization_pending":
			if err := d.wait(ctx, time.Duration(interval)*time.Second); err != nil {
				return Binding{}, err
			}
		case "slow_down":
			if interval < 30 {
				interval += 5
			}
			if err := d.wait(ctx, time.Duration(interval)*time.Second); err != nil {
				return Binding{}, err
			}
		default:
			desc := poll.ErrorDescription
			if desc == "" {
				desc = poll.Error
			}
			return Binding{}, fmt.Errorf("飞书绑定失败：%s", desc)
		}
	}
}
