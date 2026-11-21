package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	photosApp        = "com.google.android.apps.photos"
	photosSignature  = "24bb24c05e47e0aefa68a58a766179d9b613a600"
	playSignature    = "38918a453d07199354f8b19af05ec6562ced5788"
	playVersion      = "240913000"
	authExchange     = "https://android.clients.google.com/auth"
	authRefresh      = "https://android.googleapis.com/auth"
	embeddedSetup    = "https://accounts.google.com/EmbeddedSetup"
	photosRPC        = "https://photosdata-pa.googleapis.com/6439526531001121323/"
	libraryEndpoint  = photosRPC + "18047484249733410717"
	hashEndpoint     = photosRPC + "5084965799730810217"
	commitEndpoint   = photosRPC + "16538846908252377752"
	trashEndpoint    = photosRPC + "17490284929287180316"
	captionEndpoint  = photosRPC + "1552790390512470739"
	downloadEndpoint = "https://photosdata-pa.googleapis.com/$rpc/social.frontend.photos.preparedownloaddata.v1.PhotosPrepareDownloadDataService/PhotosPrepareDownload"
	uploadEndpoint   = "https://photos.googleapis.com/data/upload/uploadmedia/interactive"
)

type HTTPError struct{ Code int }

func (e HTTPError) Error() string { return fmt.Sprintf("Google HTTP %d", e.Code) }

type Photos struct {
	client                         *http.Client
	credentials                    url.Values
	agent, lang, model, deviceMake string
	apk, android                   int64
	authMu                         sync.Mutex
	token                          string
	expiry                         int64
	paceMu                         sync.Mutex
	nextRequest                    time.Time
}

func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 60 * time.Second
	t.MaxIdleConnsPerHost = 16
	return t
}

func newPhotos(options PhotoConfig) (*Photos, error) {
	credentials, err := url.ParseQuery(options.AuthData)
	if err != nil || credentials.Get("androidId") == "" || credentials.Get("Token") == "" {
		return nil, errors.New("invalid auth_data. Run googlephotosfs config")
	}
	options.defaults()
	if options.UnlimitedUpload {
		options.DeviceModel, options.DeviceMake, options.AndroidAPI = "Pixel XL", "Google", 28
	}
	lang := credentials.Get("lang")
	if lang == "" {
		lang = "en_US"
	}
	agent := fmt.Sprintf("%s/%d (Linux; U; Android 9; %s; %s; Build/PQ2A.190205.001; Cronet/127.0.6510.5) (gzip)",
		photosApp, options.APKVersion, lang, options.DeviceModel)
	return &Photos{
		client: &http.Client{Transport: transport()}, credentials: credentials, agent: agent, lang: lang,
		model: options.DeviceModel, deviceMake: options.DeviceMake, apk: options.APKVersion, android: options.AndroidAPI,
	}, nil
}

func formResponse(content []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	return values
}

func exchange(ctx context.Context, token string) (string, error) {
	client := http.Client{
		Transport: transport(), Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	return exchangeCookie(ctx, &client, token)
}

func exchangeCookie(ctx context.Context, client *http.Client, token string) (string, error) {
	token = strings.TrimPrefix(strings.TrimSpace(token), "oauth_token=")
	if !strings.HasPrefix(token, "oauth2_4/") {
		return "", errors.New("oauth_token must start with oauth2_4/")
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	androidID := hex.EncodeToString(id[:])
	form := url.Values{
		"accountType": {"HOSTED_OR_GOOGLE"}, "Email": {"oauth-token@example.com"}, "has_permission": {"1"},
		"add_account": {"1"}, "ACCESS_TOKEN": {"1"}, "Token": {token}, "service": {"ac2dm"}, "source": {"android"},
		"androidId": {androidID}, "device_country": {"us"}, "operatorCountry": {"us"}, "lang": {"en"},
		"sdk_version": {"17"}, "google_play_services_version": {playVersion}, "client_sig": {playSignature},
		"callerSig": {playSignature}, "droidguard_results": {"dummy123"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authExchange, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "GoogleAuth/1.4")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", HTTPError{resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return "", err
	}
	fields := formResponse(data)
	if fields["Error"] != "" {
		return "", fmt.Errorf("Google rejected the sign-in: %s", fields["Error"])
	}
	if fields["Token"] == "" || fields["Email"] == "" {
		return "", errors.New("authentication response is missing an account or token")
	}
	return (url.Values{
		"androidId": {androidID}, "app": {photosApp}, "callerPkg": {photosApp}, "callerSig": {photosSignature},
		"client_sig": {photosSignature}, "device_country": {"us"}, "Email": {fields["Email"]},
		"google_play_services_version": {playVersion}, "lang": {"en_US"}, "oauth2_foreground": {"1"},
		"operatorCountry": {"us"}, "sdk_version": {"33"},
		"service": {"oauth2:openid https://www.googleapis.com/auth/mobileapps.native https://www.googleapis.com/auth/photos.native"},
		"source":  {"android"}, "Token": {fields["Token"]},
	}).Encode(), nil
}

func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
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

func canRetry(err error) bool {
	var he HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case 429, 500, 502, 503, 504, 509:
			return true
		}
		return false
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (p *Photos) request(ctx context.Context, method, address string, body func() io.Reader, length int64,
	headers http.Header, replay bool) (*http.Response, error) {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		p.paceMu.Lock()
		slot := p.nextRequest
		if now := time.Now(); slot.Before(now) {
			slot = now
		}
		p.nextRequest = slot.Add(100 * time.Millisecond)
		p.paceMu.Unlock()
		if err = pause(ctx, time.Until(slot)); err != nil {
			return nil, err
		}
		var reader io.Reader
		if body != nil {
			reader = body()
		}
		req, err := http.NewRequestWithContext(ctx, method, address, reader)
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()
		if length >= 0 {
			req.ContentLength = length
		}
		if reader != nil && length == 0 {
			req.Body = http.NoBody
		}
		response, failure := p.client.Do(req)
		if failure == nil {
			if response.StatusCode/100 == 2 {
				return response, nil
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			failure = HTTPError{response.StatusCode}
		}
		err = failure
		if !replay || !canRetry(err) || attempt == 9 {
			return nil, err
		}
		if err = pause(ctx, min(100*time.Millisecond<<attempt, 8*time.Second)); err != nil {
			return nil, err
		}
	}
	return nil, err
}

func consume(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		body = reader
	}
	const limit = 128 << 20
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if len(raw) > limit {
		return nil, errors.New("protobuf response is too large")
	}
	return raw, err
}

func (p *Photos) bearer(ctx context.Context) (string, error) {
	p.authMu.Lock()
	defer p.authMu.Unlock()
	if p.token != "" && p.expiry > time.Now().Unix() {
		return p.token, nil
	}
	form := url.Values{"app": {photosApp}, "callerPkg": {photosApp}}
	for _, k := range []string{"androidId", "client_sig", "callerSig", "device_country", "Email", "google_play_services_version", "lang", "oauth2_foreground", "sdk_version", "service", "Token"} {
		form.Set(k, p.credentials.Get(k))
	}
	headers := http.Header{"Accept-Encoding": {"gzip"}, "Content-Type": {"application/x-www-form-urlencoded"},
		"User-Agent": {"GoogleAuth/1.4 (Pixel XL PQ2A.190205.001); gzip"}, "App": {photosApp},
		"Device": {form.Get("androidId")}, "Connection": {"Keep-Alive"}}
	value := form.Encode()
	resp, err := p.request(ctx, http.MethodPost, authRefresh, func() io.Reader { return strings.NewReader(value) }, int64(len(value)), headers, true)
	if err != nil {
		return "", fmt.Errorf("mobile authentication: %w", err)
	}
	raw, err := consume(resp)
	if err != nil {
		return "", err
	}
	fields := formResponse(raw)
	if fields["Auth"] == "" {
		if fields["Error"] != "" {
			return "", fmt.Errorf("Google Auth: %s", fields["Error"])
		}
		return "", errors.New("Google Auth response is missing a token")
	}
	p.token = fields["Auth"]
	p.expiry, _ = strconv.ParseInt(fields["Expiry"], 10, 64)
	return p.token, nil
}

func (p *Photos) headers(ctx context.Context) (http.Header, error) {
	token, err := p.bearer(ctx)
	if err != nil {
		return nil, err
	}
	return http.Header{"Authorization": {"Bearer " + token}, "User-Agent": {p.agent},
		"Accept-Language": {p.lang}, "Accept-Encoding": {"gzip"}}, nil
}

func (p *Photos) proto(ctx context.Context, endpoint string, data []byte) ([]byte, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return nil, err
	}
	headers.Set("Content-Type", "application/x-protobuf")
	headers.Set("x-goog-ext-173412678-bin", "CgcIAhClARgC")
	headers.Set("x-goog-ext-174067345-bin", "CgIIAg==")
	response, err := p.request(ctx, http.MethodPost, endpoint, func() io.Reader { return bytes.NewReader(data) }, int64(len(data)), headers, true)
	if err != nil {
		return nil, err
	}
	return consume(response)
}

func (p *Photos) library(ctx context.Context, state, page string, initial bool) (LibraryPage, error) {
	var req []byte
	switch {
	case page == "":
		req = libraryState(state)
	case initial:
		req = libraryInitialPage(page)
	default:
		req = libraryDeltaPage(page, state)
	}
	raw, err := p.proto(ctx, libraryEndpoint, req)
	if err != nil {
		return LibraryPage{}, err
	}
	return parsePage(raw)
}

func (p *Photos) lookupHash(ctx context.Context, digest []byte) (string, error) {
	raw, err := p.proto(ctx, hashEndpoint, checkHashRequest(digest))
	if err != nil {
		return "", err
	}
	reply, err := decode(raw)
	return reply.sub(1, 2, 2).str(1), err
}

func (p *Photos) upload(ctx context.Context, reader io.Reader, size int64, name string, digest []byte) (string, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return "", err
	}
	headers.Set("Content-Type", "application/x-protobuf")
	headers.Set("X-Goog-Hash", "sha1="+base64.StdEncoding.EncodeToString(digest))
	headers.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	body := message(v(1, 2), v(2, 2), v(3, 1), v(4, 3), v(7, uint64(size)))
	res, err := p.request(ctx, http.MethodPost, uploadEndpoint, func() io.Reader { return bytes.NewReader(body) }, int64(len(body)), headers, true)
	if err != nil {
		return "", err
	}
	session := res.Header.Get("X-GUploader-UploadID")
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	if session == "" {
		return "", errors.New("Google did not return an upload session")
	}
	headers, err = p.headers(ctx)
	if err != nil {
		return "", err
	}
	res, err = p.request(ctx, http.MethodPut, uploadEndpoint+"?upload_id="+url.QueryEscape(session), func() io.Reader { return reader }, size, headers, false)
	if err != nil {
		return "", err
	}
	reply, err := consume(res)
	if err != nil {
		return "", err
	}
	raw, err := p.proto(ctx, commitEndpoint, commitRequest(reply, name, digest, p.model, p.deviceMake, p.android, time.Now()))
	if err != nil {
		return "", err
	}
	commit, err := decode(raw)
	if err != nil {
		return "", err
	}
	if key := commit.sub(1, 3).str(1); key != "" {
		return key, nil
	}
	return "", errors.New("Google rejected the upload without returning a key")
}

func (p *Photos) caption(ctx context.Context, key, value string) error {
	_, err := p.proto(ctx, captionEndpoint, message(text(2, value), text(3, key)))
	return err
}
func (p *Photos) trash(ctx context.Context, keys ...string) error {
	_, err := p.proto(ctx, trashEndpoint, trashRequest(keys, p.apk, p.android))
	return err
}

func (p *Photos) download(ctx context.Context, key string) (io.ReadCloser, error) {
	raw, err := p.proto(ctx, downloadEndpoint, downloadRequest(key))
	if err != nil {
		return nil, err
	}
	data, err := decode(raw)
	if err != nil {
		return nil, err
	}
	container := data.sub(1, 5, 2)
	if container == nil {
		container = data.sub(1, 5, 3)
	}
	address := container.str(6)
	if address == "" {
		address = container.str(5)
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, errors.New("Google did not return an HTTPS download URL")
	}
	host := strings.ToLower(u.Hostname())
	googleHost := host == "google.com" || strings.HasSuffix(host, ".google.com") ||
		host == "googleapis.com" || strings.HasSuffix(host, ".googleapis.com") ||
		host == "googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com") ||
		host == "gstatic.com" || strings.HasSuffix(host, ".gstatic.com")
	if !googleHost {
		return nil, errors.New("Google returned an unrecognized download server")
	}
	headers, err := p.headers(ctx)
	if err != nil {
		return nil, err
	}
	headers.Del("Accept-Encoding")
	res, err := p.request(ctx, http.MethodGet, address, nil, -1, headers, true)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

type Usage struct {
	Objects *int64 `json:"objects,omitempty"`
	Total   *int64 `json:"total,omitempty"`
	Used    *int64 `json:"used,omitempty"`
	Free    *int64 `json:"free,omitempty"`
}

func (p *Photos) usage(ctx context.Context) (Usage, error) {
	raw, err := p.proto(ctx, libraryEndpoint, libraryState(""))
	if err != nil {
		return Usage{}, err
	}
	reply, err := decode(raw)
	if err != nil {
		return Usage{}, err
	}
	if reply.sub(1) == nil {
		return Usage{}, errors.New("invalid quota response")
	}
	acc := reply.sub(1, 4)
	var usage Usage
	if objects := acc.sub(5); objects != nil {
		count := objects.num(1)
		usage.Objects = &count
	}
	if quota := acc.sub(8); quota.num(2) > 0 {
		total, used := quota.num(2), quota.num(1)
		free := total - used
		usage.Total, usage.Used, usage.Free = &total, &used, &free
	}
	return usage, nil
}
