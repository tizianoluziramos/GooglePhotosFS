package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func configure(ctx context.Context, cfg Config, args []string) error {
	input := bufio.NewReader(os.Stdin)
	prompt := func(label string) (string, error) {
		fmt.Print(label)
		value, err := input.ReadString('\n')
		if err != nil && len(value) == 0 {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}
	mode := ""
	if len(args) > 1 {
		return errors.New("usage: googlephotosfs config [manual|auto|existing|path]")
	}
	if len(args) == 1 {
		mode = args[0]
	}

	if mode == "" {
		fmt.Println("1) Manual   2) Automatic browser sign-in (Linux)   3) Existing auth_data")
		answer, err := prompt("Method [1]: ")
		if err != nil {
			return err
		}
		switch answer {
		case "", "1":
			mode = "manual"
		case "2":
			mode = "auto"
		case "3":
			mode = "existing"
		default:
			return errors.New("invalid authentication method")
		}
	}
	if mode != "manual" && mode != "auto" && mode != "existing" {
		return errors.New("invalid authentication method")
	}
	var secret string
	var err error
	if mode == "auto" {
		secret, err = browserLogin(ctx)
	} else {
		if mode == "manual" {
			fmt.Println("Open " + embeddedSetup + ", sign in, and accept.")
			fmt.Println("In the developer tools, find the oauth_token cookie for accounts.google.com.")
		}
		secretName := "oauth_token"
		if mode == "existing" {
			secretName = "auth_data"
		}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Print(secretName + ": ")
			raw, readErr := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			secret, err = strings.TrimSpace(string(raw)), readErr
		} else {
			secret, err = prompt(secretName + ": ")
		}
	}
	if err != nil {
		return err
	}
	if mode == "existing" || strings.Contains(secret, "androidId=") {
		cfg.Photos.AuthData = secret
	} else {
		cfg.Photos.AuthData, err = exchange(ctx, secret)
		if err != nil {
			return err
		}
	}
	unlimited, err := prompt(fmt.Sprintf("Use the Pixel XL profile for uploads [%t]: ", cfg.Photos.UnlimitedUpload))
	if err != nil {
		return err
	}
	if unlimited != "" {
		cfg.Photos.UnlimitedUpload, err = strconv.ParseBool(unlimited)
		if err != nil {
			return err
		}
	}
	fmt.Println("Available list modes: flat, calendar, month, year, description_path")
	list, err := prompt("Mode [" + cfg.Photos.ListMode + "]: ")
	if err != nil {
		return err
	}
	if list != "" {
		cfg.Photos.ListMode = list
	}
	api, err := newPhotos(cfg.Photos)
	if err != nil {
		return err
	}
	defer api.client.CloseIdleConnections()
	if _, err = api.bearer(ctx); err != nil {
		return err
	}

	if err = cfg.save(); err != nil {
		return err
	}
	filename, _ := configPath()
	fmt.Printf("Sign-in verified for %s. Configuration saved to %s\n", api.credentials.Get("Email"), filename)
	return nil
}

func browserLogin(ctx context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("automatic mode requires Linux. Use manual mode")
	}
	var browser string
	for _, program := range []string{"google-chrome", "google-chrome-beta", "chrome", "chromium", "chromium-browser", "brave", "brave-browser"} {
		if filename, err := exec.LookPath(program); err == nil {
			browser = filename
			break
		}
	}
	if browser == "" {
		return "", errors.New("could not find Chrome, Chromium, or Brave")
	}
	profile, err := os.MkdirTemp("", "googlephotosfs-auth-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(profile)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1",
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", embeddedSetup)
	if err = cmd.Start(); err != nil {
		return "", err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	fmt.Println("Sign in in the browser window.")
	for ctx.Err() == nil {
		raw, _ := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		port, _, _ := strings.Cut(string(raw), "\n")
		if number, err := strconv.Atoi(port); err == nil && number > 0 && number <= 65535 {
			if token := browserCookie(ctx, port); token != "" {
				return token, nil
			}
		}
		if err = pause(ctx, 1200*time.Millisecond); err != nil {
			return "", err
		}
	}
	return "", ctx.Err()
}

func browserCookie(ctx context.Context, port string) string {
	httpClient := http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/json/list", nil)
	if err != nil {
		return ""
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var targets []struct {
		Kind      string `json:"type"`
		URL       string `json:"url"`
		Websocket string `json:"webSocketDebuggerUrl"`
	}
	if json.NewDecoder(resp.Body).Decode(&targets) != nil {
		return ""
	}
	for _, target := range targets {
		address, err := url.Parse(target.URL)
		socket, wsErr := url.Parse(target.Websocket)
		if err != nil || wsErr != nil || target.Kind != "page" || address.Scheme != "https" || address.Hostname() != "accounts.google.com" ||
			socket.Scheme != "ws" || (socket.Hostname() != "127.0.0.1" && socket.Hostname() != "localhost") || socket.Port() != port {
			continue
		}
		dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
		conn, res, err := dialer.DialContext(ctx, target.Websocket, nil)
		if err != nil {
			if res != nil {
				res.Body.Close()
			}
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.SetReadLimit(1 << 20)
		token := readBrowserCookie(conn)
		conn.Close()
		if token != "" {
			return token
		}
	}
	return ""
}

func readBrowserCookie(conn *websocket.Conn) string {
	err := conn.WriteJSON(map[string]any{"id": 1, "method": "Network.getCookies", "params": map[string]any{"urls": []string{embeddedSetup}}})
	for err == nil {
		var reply struct {
			ID     int `json:"id"`
			Result struct {
				Cookies []struct{ Name, Value string } `json:"cookies"`
			} `json:"result"`
		}
		err = conn.ReadJSON(&reply)
		if err != nil {
			return ""
		}
		if reply.ID != 1 {
			continue
		}
		for _, cookie := range reply.Result.Cookies {
			if cookie.Name == "oauth_token" && strings.HasPrefix(cookie.Value, "oauth2_4/") {
				return cookie.Value
			}
		}
		return ""
	}
	return ""
}
