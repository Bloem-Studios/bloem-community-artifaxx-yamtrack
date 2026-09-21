package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/Bloem-Studios/bloem-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

const maxErrorBodyBytes = 4 << 10

type Provider struct {
	pluginv1.UnimplementedWatchSyncProviderServer
	client *http.Client
}

func NewProvider(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Provider{client: client}
}

func (p *Provider) InitAuthorize(context.Context, *pluginv1.WatchSyncInitAuthorizeRequest) (*pluginv1.WatchSyncInitAuthorizeResponse, error) {
	return &pluginv1.WatchSyncInitAuthorizeResponse{Fault: oauthUnsupportedFault()}, nil
}

func (p *Provider) ExchangeCode(context.Context, *pluginv1.WatchSyncExchangeCodeRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	return &pluginv1.WatchSyncCredentialResponse{Fault: oauthUnsupportedFault()}, nil
}

func (p *Provider) ExchangeAPIKey(ctx context.Context, req *pluginv1.WatchSyncExchangeAPIKeyRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	webhookURL, account, err := parseWebhookURL(req.GetApiKey())
	if err != nil {
		return &pluginv1.WatchSyncCredentialResponse{Fault: faultFromError(err)}, nil
	}
	if err := p.probe(ctx, webhookURL); err != nil {
		return &pluginv1.WatchSyncCredentialResponse{Fault: faultFromError(err)}, nil
	}
	return &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: webhookURL},
		Account:     protoAccount(account),
	}, nil
}

func (p *Provider) RefreshCredentials(_ context.Context, req *pluginv1.WatchSyncRefreshCredentialsRequest) (*pluginv1.WatchSyncCredentialResponse, error) {
	webhookURL, account, err := parseWebhookURL(req.GetContext().GetCredentials().GetAccessToken())
	if err != nil {
		return &pluginv1.WatchSyncCredentialResponse{Fault: faultFromError(err)}, nil
	}
	return &pluginv1.WatchSyncCredentialResponse{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: webhookURL},
		Account:     protoAccount(account),
	}, nil
}

func (p *Provider) GetAccount(_ context.Context, req *pluginv1.WatchSyncGetAccountRequest) (*pluginv1.WatchSyncGetAccountResponse, error) {
	_, account, err := parseWebhookURL(req.GetContext().GetCredentials().GetAccessToken())
	if err != nil {
		return &pluginv1.WatchSyncGetAccountResponse{Fault: faultFromError(err)}, nil
	}
	return &pluginv1.WatchSyncGetAccountResponse{Account: protoAccount(account)}, nil
}

func (p *Provider) ApplyEvents(ctx context.Context, req *pluginv1.WatchSyncApplyEventsRequest) (*pluginv1.WatchSyncApplyEventsResponse, error) {
	webhookURL := strings.TrimSpace(req.GetContext().GetCredentials().GetAccessToken())
	if webhookURL == "" {
		return &pluginv1.WatchSyncApplyEventsResponse{
			Fault: invalidCredentialFault("yamtrack webhook URL is missing"),
		}, nil
	}
	results := make([]*pluginv1.WatchSyncApplyResult, 0, len(req.GetEvents()))
	for _, event := range req.GetEvents() {
		result := p.applyEvent(ctx, webhookURL, event)
		if fault := result.GetFault(); fault != nil &&
			fault.GetCode() == pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL {
			return &pluginv1.WatchSyncApplyEventsResponse{Fault: fault}, nil
		}
		results = append(results, result)
	}
	return &pluginv1.WatchSyncApplyEventsResponse{Results: results}, nil
}

func (p *Provider) ListRemoteState(context.Context, *pluginv1.WatchSyncListRemoteStateRequest) (*pluginv1.WatchSyncListRemoteStateResponse, error) {
	return &pluginv1.WatchSyncListRemoteStateResponse{CompleteSnapshot: true}, nil
}

func (p *Provider) applyEvent(ctx context.Context, webhookURL string, event *pluginv1.WatchSyncEvent) *pluginv1.WatchSyncApplyResult {
	result := &pluginv1.WatchSyncApplyResult{EventId: event.GetEventId()}
	switch event.GetOperation() {
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE:
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE
		return result
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START:
		return p.scrobble(ctx, webhookURL, event, "Play", false)
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP:
		return p.scrobble(ctx, webhookURL, event, "Stop", eventPlayed(event))
	default:
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
		result.Fault = &pluginv1.WatchSyncFault{
			Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMANENT,
			SafeMessage: "yamtrack is scrobble-only",
		}
		return result
	}
}

func (p *Provider) scrobble(
	ctx context.Context,
	webhookURL string,
	event *pluginv1.WatchSyncEvent,
	jellyfinEvent string,
	played bool,
) *pluginv1.WatchSyncApplyResult {
	result := &pluginv1.WatchSyncApplyResult{EventId: event.GetEventId()}
	payload, err := buildJellyfinPayload(event, jellyfinEvent, played)
	if err != nil {
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
		result.Fault = faultFromError(err)
		return result
	}
	body, err := json.Marshal(payload)
	if err != nil {
		result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY
		result.Fault = temporaryFault("encode yamtrack webhook payload")
		return result
	}
	if err := p.post(ctx, webhookURL, body, false); err != nil {
		result.Fault = faultFromError(err)
		result.Status = applyStatusForFault(result.Fault.GetCode())
		return result
	}
	result.Status = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED
	return result
}

// applyStatusForFault maps a fault onto the per-event status the host acts on.
// The proto reserves RETRY for TEMPORARY and RATE_LIMITED. INVALID_CREDENTIAL is
// connection-wide, so ApplyEvents hoists it onto the response before this status
// is ever read.
func applyStatusForFault(code pluginv1.WatchSyncFaultCode) pluginv1.WatchSyncApplyStatus {
	switch code {
	case pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMANENT,
		pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMISSION_DENIED:
		return pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
	default:
		return pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_RETRY
	}
}

func (p *Provider) probe(ctx context.Context, webhookURL string) error {
	return p.post(ctx, webhookURL, nil, true)
}

func (p *Provider) post(ctx context.Context, webhookURL string, body []byte, probe bool) error {
	if strings.TrimSpace(webhookURL) == "" {
		return errInvalidCredential("yamtrack webhook URL is missing")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, reader)
	if err != nil {
		// err embeds the request URL, and that URL carries the token.
		return errInvalidRequest("yamtrack webhook URL is not a valid request URL")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))

	if probe {
		return probeStatusError(resp.StatusCode)
	}
	return postStatusError(resp)
}

// probeStatusError reads an empty-body POST as a webhook-URL check. Yamtrack
// answers an unknown token with 401 and a known one with 400 "Missing payload",
// so 400 is the success signal here.
func probeStatusError(status int) error {
	switch {
	case status == http.StatusBadRequest:
		return nil
	case status == http.StatusUnauthorized:
		return errInvalidCredential("yamtrack webhook token was rejected")
	case isRedirect(status):
		return errInvalidRequest(fmt.Sprintf(
			"yamtrack webhook URL redirects (status %d); paste the URL Yamtrack is actually served on", status))
	default:
		return errInvalidRequest(fmt.Sprintf(
			"yamtrack webhook URL did not look like a Jellyfin webhook: status %d", status))
	}
}

func postStatusError(resp *http.Response) error {
	status := resp.StatusCode
	switch {
	case status == http.StatusUnauthorized:
		return errInvalidCredential("yamtrack webhook token was rejected")
	case status == http.StatusTooManyRequests:
		return errRateLimited(
			"yamtrack rate limited the webhook request",
			parseRetryAfter(resp.Header.Get("Retry-After")),
		)
	case status == http.StatusRequestTimeout:
		return errTemporary("yamtrack webhook request timed out: status 408")
	case isRedirect(status):
		// Redirects are never followed: the token lives in the URL path and must
		// not be forwarded to another host. Retrying cannot fix that.
		return errInvalidRequest(fmt.Sprintf(
			"yamtrack webhook URL redirects (status %d); reconnect with the URL Yamtrack is served on", status))
	case status >= 400 && status < 500:
		return errInvalidRequest(fmt.Sprintf("yamtrack rejected the webhook payload: status %d", status))
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return errTemporary(fmt.Sprintf("yamtrack webhook request failed: status %d", status))
	default:
		return nil
	}
}

func isRedirect(status int) bool {
	return status >= http.StatusMultipleChoices && status < http.StatusBadRequest
}

// parseRetryAfter reads a Retry-After header in either delay-seconds or
// HTTP-date form. It returns 0 when the header is absent or unusable.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

// transportError classifies a transport failure into a fixed, non-secret
// message. The raw error is an *url.Error whose text embeds the request URL, and
// that URL carries the user's Yamtrack token in its path. safe_message is
// persisted by the host and shown to operators, so it must never see it.
func transportError(err error) error {
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return errTemporary("yamtrack webhook request was canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return errTemporary("yamtrack did not respond before the request timed out")
	case errors.As(err, &netErr) && netErr.Timeout():
		return errTemporary("yamtrack did not respond before the request timed out")
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return errPermanent("yamtrack webhook TLS certificate could not be verified")
	}
	return errTemporary("yamtrack webhook host could not be reached")
}

func protoAccount(account webhookAccount) *pluginv1.WatchSyncAccount {
	return &pluginv1.WatchSyncAccount{
		ExternalSubject: account.ID,
		Username:        account.Username,
	}
}

type classifiedError struct {
	code       pluginv1.WatchSyncFaultCode
	message    string
	retryAfter time.Duration
}

func (e classifiedError) Error() string { return e.message }

func errInvalidRequest(message string) error {
	return classifiedError{code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST, message: message}
}

func errInvalidCredential(message string) error {
	return classifiedError{code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL, message: message}
}

func errTemporary(message string) error {
	return classifiedError{code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY, message: message}
}

func errPermanent(message string) error {
	return classifiedError{code: pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMANENT, message: message}
}

func errRateLimited(message string, retryAfter time.Duration) error {
	return classifiedError{
		code:       pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_RATE_LIMITED,
		message:    message,
		retryAfter: retryAfter,
	}
}

func faultFromError(err error) *pluginv1.WatchSyncFault {
	var classified classifiedError
	if errors.As(err, &classified) {
		fault := &pluginv1.WatchSyncFault{Code: classified.code, SafeMessage: classified.message}
		if classified.retryAfter > 0 {
			fault.RetryAfter = durationpb.New(classified.retryAfter)
		}
		return fault
	}
	// Unclassified errors never reach safe_message: anything wrapping a request
	// to the webhook URL carries the user's token along with it.
	return temporaryFault("yamtrack webhook request failed")
}

func temporaryFault(message string) *pluginv1.WatchSyncFault {
	return &pluginv1.WatchSyncFault{
		Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_TEMPORARY,
		SafeMessage: message,
	}
}

func invalidCredentialFault(message string) *pluginv1.WatchSyncFault {
	return &pluginv1.WatchSyncFault{
		Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL,
		SafeMessage: message,
	}
}

func oauthUnsupportedFault() *pluginv1.WatchSyncFault {
	return &pluginv1.WatchSyncFault{
		Code:        pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST,
		SafeMessage: "yamtrack uses a Jellyfin webhook URL, not OAuth",
	}
}
