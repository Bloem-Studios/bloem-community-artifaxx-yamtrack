package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const jellyfinWebhookPath = "/webhook/jellyfin"

// completedPercent is the share of an item that must be watched for a stop to
// count as played. silo-server reports completion_percent on a 0-100 scale
// (position_seconds / duration_seconds * 100).
const completedPercent = 90

type jellyfinWebhookPayload struct {
	Event string              `json:"Event"`
	Item  jellyfinWebhookItem `json:"Item"`
}

type jellyfinWebhookItem struct {
	Type              string            `json:"Type"`
	Name              string            `json:"Name,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	IndexNumber       int               `json:"IndexNumber"`
	ProviderIds       map[string]string `json:"ProviderIds"`
	UserData          jellyfinUserData  `json:"UserData"`
}

type jellyfinUserData struct {
	Played bool `json:"Played"`
}

type webhookAccount struct {
	ID       string
	Username string
}

func parseWebhookURL(raw string) (string, webhookAccount, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", webhookAccount{}, errInvalidRequest("yamtrack jellyfin webhook URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", webhookAccount{}, errInvalidRequest("yamtrack webhook URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", webhookAccount{}, errInvalidRequest("yamtrack webhook URL must use http or https")
	}
	if parsed.User != nil {
		return "", webhookAccount{}, errInvalidRequest("yamtrack webhook URL must not embed credentials")
	}
	if parsed.Host == "" {
		return "", webhookAccount{}, errInvalidRequest("yamtrack webhook URL has no host")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	idx := strings.LastIndex(strings.ToLower(path), jellyfinWebhookPath)
	if idx < 0 {
		return "", webhookAccount{}, errInvalidRequest("paste the full Yamtrack Jellyfin webhook URL")
	}
	token := strings.TrimPrefix(path[idx+len(jellyfinWebhookPath):], "/")
	if token == "" || strings.Contains(token, "/") {
		return "", webhookAccount{}, errInvalidRequest("yamtrack webhook URL is missing its token")
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Path = path
	return parsed.String(), webhookAccount{ID: accountSubject(token), Username: parsed.Hostname()}, nil
}

// accountSubject derives a stable, non-secret identifier from the webhook token.
// The token is the user's Yamtrack credential; it belongs only in
// WatchSyncCredentials, which the host encrypts at rest. WatchSyncAccount is
// stored in the clear and shown in the UI, so it gets a digest instead.
func accountSubject(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

func buildJellyfinPayload(event *pluginv1.WatchSyncEvent, jellyfinEvent string, played bool) (jellyfinWebhookPayload, error) {
	payload := jellyfinWebhookPayload{
		Event: jellyfinEvent,
		Item: jellyfinWebhookItem{
			ProviderIds: map[string]string{},
			UserData:    jellyfinUserData{Played: played},
		},
	}
	media := event.GetMedia()
	switch media.GetMediaType() {
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE:
		tvdb := media.GetExternalIds()["tvdb"]
		imdb := media.GetExternalIds()["imdb"]
		if tvdb == "" && imdb == "" {
			return jellyfinWebhookPayload{}, errInvalidRequest("yamtrack episode scrobble needs a TVDB or IMDb id")
		}
		payload.Item.Type = "Episode"
		payload.Item.ParentIndexNumber = int(media.GetSeasonNumber())
		payload.Item.IndexNumber = int(media.GetEpisodeNumber())
		payload.Item.SeriesName = media.GetSeriesTitle()
		setProviderID(payload.Item.ProviderIds, "Tvdb", tvdb)
		setProviderID(payload.Item.ProviderIds, "Imdb", imdb)
		// Yamtrack treats Tmdb as the show id for TV.
		setProviderID(payload.Item.ProviderIds, "Tmdb", media.GetSeriesExternalIds()["tmdb"])
	default:
		tmdb := media.GetExternalIds()["tmdb"]
		imdb := media.GetExternalIds()["imdb"]
		if tmdb == "" && imdb == "" {
			return jellyfinWebhookPayload{}, errInvalidRequest("yamtrack movie scrobble needs a TMDB or IMDb id")
		}
		payload.Item.Type = "Movie"
		payload.Item.Name = media.GetTitle()
		payload.Item.ProductionYear = int(media.GetYear())
		setProviderID(payload.Item.ProviderIds, "Tmdb", tmdb)
		setProviderID(payload.Item.ProviderIds, "Imdb", imdb)
		setProviderID(payload.Item.ProviderIds, "Tvdb", media.GetExternalIds()["tvdb"])
	}
	return payload, nil
}

func setProviderID(ids map[string]string, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	ids[key] = value
}

// eventPlayed decides whether a stop finished the item.
//
// completion_percent is the path that runs in practice: silo-server builds
// scrobble events in mediaFromIdentity (internal/watchsync/plugin_provider.go)
// and never populates WatchSyncMedia.metadata. The metadata check is kept as an
// explicit override for a host that does set one, not as the primary signal.
func eventPlayed(event *pluginv1.WatchSyncEvent) bool {
	if fields := event.GetMedia().GetMetadata().GetFields(); fields != nil {
		if value, ok := fields["completed"]; ok && value.GetBoolValue() {
			return true
		}
	}
	return event.GetCompletionPercent() >= completedPercent
}
