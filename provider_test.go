package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Bloem-Studios/bloem-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestParseWebhookURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		wantURL   string
		wantToken string
		wantErr   string
	}{
		{
			name:      "https jellyfin webhook",
			raw:       "  https://yamtrack.example.com/webhook/jellyfin/tok_abc  ",
			wantURL:   "https://yamtrack.example.com/webhook/jellyfin/tok_abc",
			wantToken: "tok_abc",
		},
		{
			name:      "http with port and trailing slash",
			raw:       "http://192.168.1.12:8000/webhook/jellyfin/tok123/",
			wantURL:   "http://192.168.1.12:8000/webhook/jellyfin/tok123",
			wantToken: "tok123",
		},
		{
			name:      "subpath install",
			raw:       "https://home.example.com/yamtrack/webhook/jellyfin/token",
			wantURL:   "https://home.example.com/yamtrack/webhook/jellyfin/token",
			wantToken: "token",
		},
		{
			name:    "empty",
			raw:     "   ",
			wantErr: "yamtrack jellyfin webhook URL is required",
		},
		{
			name:    "wrong scheme",
			raw:     "ftp://yamtrack.example.com/webhook/jellyfin/tok",
			wantErr: "yamtrack webhook URL must use http or https",
		},
		{
			name:    "embedded credentials",
			raw:     "https://user:pass@yamtrack.example.com/webhook/jellyfin/tok",
			wantErr: "yamtrack webhook URL must not embed credentials",
		},
		{
			name:    "missing webhook path",
			raw:     "https://yamtrack.example.com/api/v1/media",
			wantErr: "paste the full Yamtrack Jellyfin webhook URL",
		},
		{
			name:    "missing token",
			raw:     "https://yamtrack.example.com/webhook/jellyfin/",
			wantErr: "yamtrack webhook URL is missing its token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotURL, account, err := parseWebhookURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if gotURL != tt.wantURL {
				t.Fatalf("url = %q, want %q", gotURL, tt.wantURL)
			}
			if account.ID != accountSubject(tt.wantToken) {
				t.Fatalf("account id = %q, want %q", account.ID, accountSubject(tt.wantToken))
			}
			if strings.Contains(account.ID, tt.wantToken) {
				t.Fatalf("account id %q leaks the webhook token", account.ID)
			}
		})
	}
}

func TestExchangeAPIKeyProbesEmptyPayload(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		if len(gotBody) > 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Missing payload"))
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	webhook := server.URL + "/webhook/jellyfin/test-token"
	resp, err := p.ExchangeAPIKey(context.Background(), &pluginv1.WatchSyncExchangeAPIKeyRequest{ApiKey: webhook})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.GetFault() != nil {
		t.Fatalf("fault = %#v", resp.GetFault())
	}
	if gotPath != "/webhook/jellyfin/test-token" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(gotBody) != 0 {
		t.Fatalf("probe body = %q, want empty", gotBody)
	}
	if resp.GetCredentials().GetAccessToken() != webhook {
		t.Fatalf("stored token = %q", resp.GetCredentials().GetAccessToken())
	}
	if subject := resp.GetAccount().GetExternalSubject(); subject != accountSubject("test-token") {
		t.Fatalf("account = %#v", resp.GetAccount())
	}
	if strings.Contains(resp.GetAccount().GetExternalSubject(), "test-token") {
		t.Fatalf("account subject leaks the webhook token: %#v", resp.GetAccount())
	}
}

func TestExchangeAPIKeyRejectsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ExchangeAPIKey(context.Background(), &pluginv1.WatchSyncExchangeAPIKeyRequest{
		ApiKey: server.URL + "/webhook/jellyfin/bad",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.GetFault().GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL {
		t.Fatalf("fault = %#v", resp.GetFault())
	}
}

func TestPauseIsNoOp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("pause should not call Yamtrack")
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "pause-1",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE,
			Media:     movieMedia("603", ""),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
}

func TestStopCompletedMoviePostsJellyfinPayload(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "stop-1",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			Completed: true,
			Media:     movieMedia("603", "tt0133093"),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
	if got.Event != "Stop" || got.Item.Type != "Movie" || !got.Item.UserData.Played {
		t.Fatalf("payload = %#v", got)
	}
	if got.Item.ProviderIds["Tmdb"] != "603" || got.Item.ProviderIds["Imdb"] != "tt0133093" {
		t.Fatalf("provider ids = %#v", got.Item.ProviderIds)
	}
}

func TestStopCompletedEpisodeUsesSeriesTMDB(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "stop-ep",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			Completed: true,
			Media: &pluginv1.WatchSyncMedia{
				MediaType:         pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE,
				SeasonNumber:      1,
				EpisodeNumber:     1,
				ExternalIds:       map[string]string{"tvdb": "303821", "imdb": "tt0583459", "tmdb": "62085"},
				SeriesExternalIds: map[string]string{"tmdb": "1396", "tvdb": "75930"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
	if got.Item.Type != "Episode" || got.Item.ProviderIds["Tvdb"] != "303821" {
		t.Fatalf("payload = %#v", got)
	}
	if got.Item.ProviderIds["Tmdb"] != "1396" {
		t.Fatalf("tmdb = %q, want series id", got.Item.ProviderIds["Tmdb"])
	}
}

func TestStartMovieMarksUnplayed(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	_, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "start-1",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START,
			Media:     movieMedia("603", ""),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Event != "Play" || got.Item.UserData.Played {
		t.Fatalf("payload = %#v, want Play with Played false", got)
	}
}

func TestStopIncompleteMovieMarksUnplayed(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	_, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:           "stop-incomplete",
			Operation:         pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			CompletionPercent: 12,
			Completed:         false,
			Media:             movieMedia("603", ""),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Event != "Stop" || got.Item.UserData.Played {
		t.Fatalf("payload = %#v, want Stop with Played false", got)
	}
}

func TestEpisodeWithoutTVDBOrIMDbIsRejected(t *testing.T) {
	p := NewProvider(http.DefaultClient)
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated("https://yamtrack.example.com/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "ep-tmdb-only",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			Completed: true,
			Media: &pluginv1.WatchSyncMedia{
				MediaType:         pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE,
				ExternalIds:       map[string]string{"tmdb": "1396"},
				SeriesExternalIds: map[string]string{"tmdb": "1396"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := resp.GetResults()[0]
	if got.GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED ||
		!strings.Contains(got.GetFault().GetSafeMessage(), "TVDB or IMDb") {
		t.Fatalf("result = %#v", got)
	}
}

func TestNonScrobbleIsRejected(t *testing.T) {
	p := NewProvider(http.DefaultClient)
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated("https://yamtrack.example.com/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "mark-watched",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_WATCHED,
			Media:     movieMedia("603", ""),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
}

func capturePayload(t *testing.T, got *jellyfinWebhookPayload) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func authenticated(webhookURL string) *pluginv1.WatchSyncAuthenticatedContext {
	return &pluginv1.WatchSyncAuthenticatedContext{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: webhookURL},
	}
}

func movieMedia(tmdbID, imdbID string) *pluginv1.WatchSyncMedia {
	ids := map[string]string{}
	if tmdbID != "" {
		ids["tmdb"] = tmdbID
	}
	if imdbID != "" {
		ids["imdb"] = imdbID
	}
	return &pluginv1.WatchSyncMedia{
		MediaType:   pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
		ExternalIds: ids,
	}
}

// leakToken is distinctive enough that a substring check on a fault message is
// meaningful.
const leakToken = "s3cr3t-webhook-token"

// providerClient mirrors the redirect policy NewProvider installs in
// production. httptest.Server.Client() follows redirects, so tests that assert
// on 3xx handling have to opt back out.
func providerClient(server *httptest.Server) *http.Client {
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

func stopEvent(eventID string, completion float64, completed bool) *pluginv1.WatchSyncEvent {
	return &pluginv1.WatchSyncEvent{
		EventId:           eventID,
		Operation:         pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
		CompletionPercent: completion,
		Completed:         completed,
		Media:             movieMedia("603", ""),
	}
}

// TestStopPlayedUsesHostCompleted covers the production completion signal:
// silo-server sets WatchSyncEvent.completed and does not populate
// WatchSyncMedia.metadata. A high completion_percent must not override an
// incomplete host stop.
func TestStopPlayedUsesHostCompleted(t *testing.T) {
	tests := []struct {
		name       string
		completion float64
		completed  bool
		wantPlayed bool
	}{
		{name: "host marked complete below 100 percent", completion: 80, completed: true, wantPlayed: true},
		{name: "host marked complete at 90 percent", completion: 90, completed: true, wantPlayed: true},
		{name: "host left incomplete near the end", completion: 95, completed: false, wantPlayed: false},
		{name: "abandoned early", completion: 12, completed: false, wantPlayed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got jellyfinWebhookPayload
			server := capturePayload(t, &got)
			defer server.Close()

			p := NewProvider(server.Client())
			resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
				Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
				Events:  []*pluginv1.WatchSyncEvent{stopEvent("stop-completion", tt.completion, tt.completed)},
			})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED {
				t.Fatalf("result = %#v", resp.GetResults()[0])
			}
			if got.Event != "Stop" {
				t.Fatalf("event = %q, want Stop", got.Event)
			}
			if got.Item.UserData.Played != tt.wantPlayed {
				t.Fatalf("played = %v at %.1f%% completed=%v, want %v", got.Item.UserData.Played, tt.completion, tt.completed, tt.wantPlayed)
			}
		})
	}
}

func TestEventPlayedHonorsMetadataCompleted(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{"completed": true})
	if err != nil {
		t.Fatal(err)
	}
	event := &pluginv1.WatchSyncEvent{
		Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
		Media:     &pluginv1.WatchSyncMedia{Metadata: metadata},
	}
	if !eventPlayed(event) {
		t.Fatal("metadata.completed should mark the stop as played")
	}
}

func TestScrobbleStatusByResponseCode(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantStatus pluginv1.WatchSyncApplyStatus
		wantCode   pluginv1.WatchSyncFaultCode
	}{
		{
			name:       "bad request is not retried",
			status:     http.StatusBadRequest,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		},
		{
			name:       "forbidden is not retried",
			status:     http.StatusForbidden,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		},
		{
			name:       "not found is not retried",
			status:     http.StatusNotFound,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		},
		{
			name:       "redirect is not retried",
			status:     http.StatusMovedPermanently,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		},
		{
			name:       "request timeout is retried",
			status:     http.StatusRequestTimeout,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
		},
		{
			name:       "server error is retried",
			status:     http.StatusInternalServerError,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
		},
		{
			name:       "bad gateway is retried",
			status:     http.StatusBadGateway,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
		},
		{
			name:       "rate limited is retried",
			status:     http.StatusTooManyRequests,
			wantStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY,
			wantCode:   pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if isRedirect(tt.status) {
					w.Header().Set("Location", "https://moved.example.com/webhook/jellyfin/tok")
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			p := NewProvider(providerClient(server))
			resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
				Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
				Events:  []*pluginv1.WatchSyncEvent{stopEvent("stop-status", 95, true)},
			})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			result := resp.GetResults()[0]
			if result.GetStatus() != tt.wantStatus {
				t.Fatalf("status = %v, want %v (fault %#v)", result.GetStatus(), tt.wantStatus, result.GetFault())
			}
			if result.GetFault().GetCode() != tt.wantCode {
				t.Fatalf("fault = %#v, want code %v", result.GetFault(), tt.wantCode)
			}
		})
	}
}

func TestRateLimitedFaultCarriesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events:  []*pluginv1.WatchSyncEvent{stopEvent("stop-429", 95, true)},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	fault := resp.GetResults()[0].GetFault()
	if fault.GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED {
		t.Fatalf("fault = %#v", fault)
	}
	if got := fault.GetRetryAfter().AsDuration(); got != 30*time.Second {
		t.Fatalf("retry after = %v, want 30s", got)
	}
}

func TestExchangeAPIKeyRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://moved.example.com/webhook/jellyfin/tok")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer server.Close()

	p := NewProvider(providerClient(server))
	resp, err := p.ExchangeAPIKey(context.Background(), &pluginv1.WatchSyncExchangeAPIKeyRequest{
		ApiKey: server.URL + "/webhook/jellyfin/tok",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.GetFault().GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST {
		t.Fatalf("fault = %#v", resp.GetFault())
	}
	if !strings.Contains(resp.GetFault().GetSafeMessage(), "redirects") {
		t.Fatalf("safe message = %q, want it to explain the redirect", resp.GetFault().GetSafeMessage())
	}
}

func TestUnauthorizedDuringApplyIsConnectionFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events:  []*pluginv1.WatchSyncEvent{stopEvent("stop-401", 95, true)},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetFault().GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL {
		t.Fatalf("fault = %#v, want a connection-level INVALID_CREDENTIAL", resp.GetFault())
	}
	if len(resp.GetResults()) != 0 {
		t.Fatalf("results = %#v, want none once the connection is faulted", resp.GetResults())
	}
}

// TestFaultsNeverLeakTheWebhookToken guards the safe_message contract: the
// webhook URL carries the user's Yamtrack token in its path, and the host
// persists safe_message and shows it to operators.
func TestFaultsNeverLeakTheWebhookToken(t *testing.T) {
	assertClean := func(t *testing.T, message string) {
		t.Helper()
		if message == "" {
			t.Fatal("empty fault message")
		}
		if strings.Contains(message, leakToken) {
			t.Fatalf("fault message leaks the token: %q", message)
		}
	}

	t.Run("upstream error body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Traceback: bad token " + leakToken))
		}))
		defer server.Close()

		p := NewProvider(server.Client())
		resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
			Context: authenticated(server.URL + "/webhook/jellyfin/" + leakToken),
			Events:  []*pluginv1.WatchSyncEvent{stopEvent("leak-500", 95, true)},
		})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		assertClean(t, resp.GetResults()[0].GetFault().GetSafeMessage())
	})

	t.Run("unreachable host during apply", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		webhook := server.URL + "/webhook/jellyfin/" + leakToken
		client := server.Client()
		server.Close()

		p := NewProvider(client)
		resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
			Context: authenticated(webhook),
			Events:  []*pluginv1.WatchSyncEvent{stopEvent("leak-transport", 95, true)},
		})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		result := resp.GetResults()[0]
		if result.GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY {
			t.Fatalf("status = %v, want RETRY", result.GetStatus())
		}
		assertClean(t, result.GetFault().GetSafeMessage())
	})

	t.Run("unreachable host during connect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		webhook := server.URL + "/webhook/jellyfin/" + leakToken
		client := server.Client()
		server.Close()

		p := NewProvider(client)
		resp, err := p.ExchangeAPIKey(context.Background(), &pluginv1.WatchSyncExchangeAPIKeyRequest{ApiKey: webhook})
		if err != nil {
			t.Fatalf("exchange: %v", err)
		}
		assertClean(t, resp.GetFault().GetSafeMessage())
	})
}
