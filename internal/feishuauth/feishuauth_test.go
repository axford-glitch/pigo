package feishuauth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBeginAndPoll(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("form: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch form.Get("action") {
		case "begin":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"device_code":"dc-1","verification_uri_complete":"https://accounts.feishu.cn/oauth/verify?user_code=ABCD","user_code":"ABCD","interval":5,"expires_in":300}`)
		case "poll":
			if form.Get("device_code") != "dc-1" {
				t.Errorf("device_code = %q", form.Get("device_code"))
			}
			polls++
			if polls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"authorization_pending","error_description":"wait"}`)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"client_id":"cli_app","client_secret":"sec_app","user_info":{"open_id":"ou_user"}}`)
		default:
			http.Error(w, "bad action", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	origEndpoint, origClient := registrationEndpoint, postClient
	t.Cleanup(func() {
		registrationEndpoint = origEndpoint
		postClient = origClient
	})
	registrationEndpoint = srv.URL
	postClient = srv.Client()

	begin, err := Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if begin.DeviceCode != "dc-1" || begin.UserCode != "ABCD" {
		t.Fatalf("begin = %+v", begin)
	}
	if !strings.Contains(begin.VerificationURIComp, "from=sdk") || !strings.Contains(begin.VerificationURIComp, "user_code=ABCD") {
		t.Fatalf("verification URL = %q", begin.VerificationURIComp)
	}

	pending, err := Poll(begin.DeviceCode)
	if err != nil {
		t.Fatalf("pending Poll: %v", err)
	}
	if pending.Error != "authorization_pending" {
		t.Fatalf("pending = %+v", pending)
	}

	ok, err := Poll(begin.DeviceCode)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if ok.Error != "" || ok.ClientID != "cli_app" || ok.ClientSecret != "sec_app" || ok.OpenID() != "ou_user" {
		t.Fatalf("poll = %+v open_id=%q", ok, ok.OpenID())
	}
}

func TestBeginRejectsMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"error":"","message":"nope"}`)
	}))
	defer srv.Close()
	origEndpoint, origClient := registrationEndpoint, postClient
	t.Cleanup(func() {
		registrationEndpoint = origEndpoint
		postClient = origClient
	})
	registrationEndpoint = srv.URL
	postClient = srv.Client()

	if _, err := Begin(); err == nil {
		t.Fatal("Begin succeeded without device_code")
	}
}
