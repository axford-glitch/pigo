package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/smallnest/pigo/internal/feishuauth"
)

// DefaultListen is the loopback address LuCI calls on the router.
const DefaultListen = "127.0.0.1:8765"

// BindStatus is the public view of QR binding. It never includes the device
// code, app secret, or API key.
type BindStatus struct {
	Bound      bool   `json:"bound"`
	OpenID     string `json:"open_id,omitempty"`
	Model      string `json:"model,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	APIKeySet  bool   `json:"api_key_set"`
	APIKeyHint string `json:"api_key_hint,omitempty"`
	BindState  string `json:"bind_state"`
	UserCode   string `json:"user_code,omitempty"`
	Interval   int    `json:"interval,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Binder owns one in-flight Feishu device-authorization and the saved binding.
type Binder struct {
	opt   Options
	begin func() (*feishuauth.BeginResponse, error)
	poll  func(deviceCode string) (*feishuauth.PollResponse, error)

	mu      sync.Mutex
	pending *pendingBind
	current Binding
	// onBound is called after a poll saves a new binding. It runs without b.mu.
	onBound func(Binding)
}

type pendingBind struct {
	DeviceCode string
	URL        string
	UserCode   string
	Interval   int
	Deadline   time.Time
	LastError  string
}

func newBinder(opt Options, begin func() (*feishuauth.BeginResponse, error), poll func(string) (*feishuauth.PollResponse, error)) *Binder {
	b := &Binder{opt: opt, begin: begin, poll: poll}
	if !opt.Rebind {
		if existing, err := LoadBinding(opt.BindPath); err == nil {
			b.current = existing
		}
	}
	return b
}

// Begin starts a QR binding. A saved binding is kept until Poll succeeds, so a
// cancelled scan does not drop a working bot.
func (b *Binder) Begin() (BindStatus, error) {
	if b.begin == nil {
		return BindStatus{}, errors.New("飞书绑定不可用")
	}
	resp, err := b.begin()
	if err != nil {
		return BindStatus{}, err
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = 5
	}
	expires := resp.ExpiresIn
	if expires <= 0 {
		expires = 300
	}
	b.mu.Lock()
	b.pending = &pendingBind{
		DeviceCode: resp.DeviceCode,
		URL:        resp.VerificationURIComp,
		UserCode:   resp.UserCode,
		Interval:   interval,
		Deadline:   time.Now().Add(time.Duration(expires) * time.Second),
	}
	st := b.statusLocked()
	b.mu.Unlock()
	return st, nil
}

// Poll checks the in-flight QR once. A successful scan saves the binding and
// reports it through onBound.
func (b *Binder) Poll(ctx context.Context) (BindStatus, error) {
	b.mu.Lock()
	pending := b.pending
	if pending == nil {
		st := b.statusLocked()
		b.mu.Unlock()
		return st, nil
	}
	if time.Now().After(pending.Deadline) {
		b.pending = nil
		st := b.statusLocked()
		st.BindState = "expired"
		st.Message = "二维码已过期，请重新开始绑定"
		b.mu.Unlock()
		return st, nil
	}
	code := pending.DeviceCode
	b.mu.Unlock()

	if b.poll == nil {
		return BindStatus{}, errors.New("飞书绑定不可用")
	}
	resp, err := b.poll(code)
	if err != nil {
		return BindStatus{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil || b.pending.DeviceCode != code {
		return b.statusLocked(), nil
	}
	switch resp.Error {
	case "":
		if resp.ClientID == "" || resp.ClientSecret == "" {
			b.pending.LastError = "飞书没有返回 app id 和 app secret"
			st := b.statusLocked()
			st.BindState = "error"
			st.Message = b.pending.LastError
			return st, nil
		}
		saved := Binding{AppID: resp.ClientID, AppSecret: resp.ClientSecret, OpenID: resp.OpenID()}
		if err := SaveBinding(b.opt.BindPath, saved); err != nil {
			return BindStatus{}, err
		}
		b.current = saved
		b.pending = nil
		notify := b.onBound
		b.mu.Unlock()
		if notify != nil {
			notify(saved)
		}
		b.mu.Lock()
		return b.statusLocked(), nil
	case "authorization_pending":
		return b.statusLocked(), nil
	case "slow_down":
		if b.pending.Interval < 30 {
			b.pending.Interval += 5
		}
		return b.statusLocked(), nil
	default:
		msg := resp.ErrorDescription
		if msg == "" {
			msg = resp.Error
		}
		b.pending.LastError = msg
		st := b.statusLocked()
		st.BindState = "error"
		st.Message = msg
		return st, nil
	}
}

// QR returns a PNG of the in-flight verification URL.
func (b *Binder) QR() ([]byte, error) {
	b.mu.Lock()
	pending := b.pending
	b.mu.Unlock()
	if pending == nil || pending.URL == "" {
		return nil, errors.New("没有进行中的绑定")
	}
	return qrcode.Encode(pending.URL, qrcode.Medium, 280)
}

func (b *Binder) status(svc *Service) BindStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.statusLocked()
	if svc != nil {
		_, settings := svc.snapshot()
		if settings.Model != "" {
			st.Model = settings.Model
		}
		if settings.BaseURL != "" {
			st.BaseURL = settings.BaseURL
		}
		if settings.Protocol != "" {
			st.Protocol = settings.Protocol
		}
		if settings.APIKey != "" {
			st.APIKeySet = true
			st.APIKeyHint = maskKey(settings.APIKey)
		}
	}
	return st
}

func (b *Binder) statusLocked() BindStatus {
	st := BindStatus{BindState: "idle"}
	if b.current.usable() {
		st.Bound = true
		st.OpenID = b.current.OpenID
		st.BindState = "bound"
	}
	if b.pending != nil {
		st.BindState = "pending"
		st.UserCode = b.pending.UserCode
		st.Interval = b.pending.Interval
		if b.pending.LastError != "" {
			st.Message = b.pending.LastError
		}
	}
	return st
}

func (b *Binder) verificationURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		return ""
	}
	return b.pending.URL
}

// Control is the loopback HTTP API LuCI uses.
type Control struct {
	Binder *Binder
	Svc    *Service
	Listen string
}

func (c *Control) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", c.onlyLoopback(c.handleStatus))
	mux.HandleFunc("/v1/bind/begin", c.onlyLoopback(c.handleBegin))
	mux.HandleFunc("/v1/bind/poll", c.onlyLoopback(c.handlePoll))
	mux.HandleFunc("/v1/bind/qr.png", c.onlyLoopback(c.handleQR))
	mux.HandleFunc("/v1/settings", c.onlyLoopback(c.handleSettings))
	return mux
}

func (c *Control) Serve(ctx context.Context) error {
	if err := loopbackListen(c.Listen); err != nil {
		return err
	}
	srv := &http.Server{Addr: c.Listen, Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (c *Control) onlyLoopback(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requestLoopback(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (c *Control) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, c.Binder.status(c.Svc))
}

func (c *Control) handleBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	st, err := c.Binder.Begin()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (c *Control) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	st, err := c.Binder.Poll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if st.Bound && c.Svc != nil {
		c.Svc.setBinding(c.Binder.currentBinding())
	}
	writeJSON(w, http.StatusOK, st)
}

func (c *Control) handleQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	png, err := c.Binder.QR()
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (c *Control) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if c.Svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务还没准备好"})
		return
	}
	var body struct {
		BaseURL *string `json:"base_url"`
		APIKey  *string `json:"api_key"`
		Model   *string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是 JSON"})
		return
	}
	var notes []string
	if body.BaseURL != nil && strings.TrimSpace(*body.BaseURL) != "" {
		notes = append(notes, c.Svc.setBase(*body.BaseURL))
	}
	if body.APIKey != nil && strings.TrimSpace(*body.APIKey) != "" {
		notes = append(notes, c.Svc.setKey(*body.APIKey))
	}
	if body.Model != nil && strings.TrimSpace(*body.Model) != "" {
		notes = append(notes, c.Svc.setModel(*body.Model))
	}
	st := c.Binder.status(c.Svc)
	st.Message = strings.Join(notes, "\n")
	writeJSON(w, http.StatusOK, st)
}

func (b *Binder) currentBinding() Binding {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func requestLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loopbackListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址 %q 需要写成 host:port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("飞书配置接口只能监听本机地址，拒绝 %s", addr)
	}
	return nil
}
