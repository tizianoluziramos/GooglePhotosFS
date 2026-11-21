package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestCredentialExchange(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Test-Origin") != "android.clients.google.com" || r.URL.Path != "/auth" {
			t.Errorf("unexpected authentication route %s %s", r.Method, r.URL)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		id, err := hex.DecodeString(r.Form.Get("androidId"))
		if err != nil || len(id) != 8 || r.Form.Get("Token") != "oauth2_4/test-input" || r.Form.Get("service") != "ac2dm" || r.Form.Get("callerSig") != playSignature {
			t.Error("mobile exchange form differs")
		}
		fmt.Fprint(w, "Token=exchange-result\nEmail=test@example.com\n")
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	client := &http.Client{Transport: fixtureTransport{url: origin, base: server.Client().Transport}}
	encoded, err := exchangeCookie(ctx, client, "oauth_token=oauth2_4/test-input")
	if err != nil {
		t.Fatal(err)
	}
	form, err := url.ParseQuery(encoded)
	if err != nil || form.Get("Token") != "exchange-result" || form.Get("Email") != "test@example.com" || form.Get("app") != photosApp ||
		form.Get("callerSig") != photosSignature || !strings.Contains(form.Get("service"), "photos.native") {
		t.Fatalf("unexpected device credentials: %v", err)
	}
	if _, err := exchangeCookie(ctx, client, "invalid"); err == nil {
		t.Fatal("malformed cookie accepted")
	}
}

func TestBrowserCookieScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"type": "page", "url": "https://example.org", "webSocketDebuggerUrl": "ws://" + r.Host + "/unexpected"},
				{"type": "page", "url": embeddedSetup, "webSocketDebuggerUrl": "ws://" + r.Host + "/accounts"},
			})
		case "/accounts":
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			var request struct {
				ID     int
				Method string
				Params struct{ URLs []string }
			}
			if err := conn.ReadJSON(&request); err != nil {
				t.Error(err)
				return
			}
			if request.Method != "Network.getCookies" || len(request.Params.URLs) != 1 || request.Params.URLs[0] != embeddedSetup {
				t.Error("cookie scope differs")
			}
			_ = conn.WriteJSON(map[string]any{"method": "Page.loadEventFired"})
			_ = conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{"cookies": []map[string]string{
				{"name": "other", "value": "oauth2_4/ignored"},
				{"name": "oauth_token", "value": "oauth2_4/generated-for-test"},
			}}})
		default:
			t.Error("unrelated browser target accessed")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if got := browserCookie(context.Background(), port); got != "oauth2_4/generated-for-test" {
		t.Fatalf("browser returned unexpected cookie: %q", got)
	}
}
