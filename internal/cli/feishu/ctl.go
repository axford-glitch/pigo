package feishu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Ctl is `pigo feishu-ctl`, the loopback client LuCI runs on the router.
// It talks to the pigo --feishu process and never prints the API key or app secret.
func Ctl(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: pigo feishu-ctl status|qr|bind-begin|bind-poll|set")
		return 2
	}
	base := os.Getenv("PIGO_FEISHU_URL")
	if base == "" {
		base = "http://" + DefaultListen
	}
	base = strings.TrimRight(base, "/")
	switch args[0] {
	case "status":
		return ctlJSON(http.MethodGet, base+"/v1/status", nil, out, errOut)
	case "bind-begin":
		return ctlJSON(http.MethodPost, base+"/v1/bind/begin", []byte("{}"), out, errOut)
	case "bind-poll":
		return ctlJSON(http.MethodPost, base+"/v1/bind/poll", []byte("{}"), out, errOut)
	case "qr":
		return ctlQR(base, out, errOut)
	case "set":
		return ctlSet(args[1:], base, out, errOut)
	default:
		fmt.Fprintf(errOut, "pigo: unknown feishu-ctl command %q\n", args[0])
		return 2
	}
}

func ctlJSON(method, url string, body []byte, out, errOut io.Writer) int {
	raw, code, err := ctlDo(method, url, body)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if _, err := out.Write(raw); err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(out)
	}
	if code >= 400 {
		return 1
	}
	return 0
}

func ctlQR(base string, out, errOut io.Writer) int {
	raw, code, err := ctlDo(http.MethodGet, base+"/v1/bind/qr.png", nil)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if code >= 400 {
		fmt.Fprintf(errOut, "pigo: %s\n", strings.TrimSpace(string(raw)))
		return 1
	}
	fmt.Fprintln(out, base64.StdEncoding.EncodeToString(raw))
	return 0
}

func ctlSet(args []string, base string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	fs.SetOutput(errOut)
	baseURL := fs.String("base", "", "model API base URL")
	model := fs.String("model", "", "model id")
	key := fs.String("key", "", "API key")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	payload := map[string]string{}
	if *baseURL != "" {
		payload["base_url"] = *baseURL
	}
	if *model != "" {
		payload["model"] = *model
	}
	if *key != "" {
		payload["api_key"] = *key
	}
	if len(payload) == 0 {
		fmt.Fprintln(errOut, "usage: pigo feishu-ctl set --base URL --model ID --key KEY")
		return 2
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	return ctlJSON(http.MethodPost, base+"/v1/settings", raw, out, errOut)
}

func ctlDo(method, url string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连不上飞书配置接口（%s）。先运行 pigo --feishu：%w", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}
