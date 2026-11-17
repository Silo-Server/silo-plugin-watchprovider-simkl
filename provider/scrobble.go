package provider

import (
	"context"
	"net/http"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/hashicorp/go-hclog"
)

// scrobble forwards a playback start, pause, or stop. Simkl answers a
// completed stop it already recorded, for example a redelivered one, with
// 409 already_watched, which means the stop is applied.
//
// With the profile's "Log rewatches" setting on, a stop of finished playback
// is sent with allow_rewatch=yes, so finishing a title the account already
// watched is recorded as a rewatch instead of ignored, as markWatched does.
// Simkl ignores the flag on a first watch and absorbs a repeat within two days
// of the last watch. An unfinished stop goes without the flag even when its
// progress passes Simkl's watched threshold, so only playback Silo counts as
// finished logs a rewatch. The flag never goes on a start: Simkl can apply it
// there to a different, earlier playback.
func (s *Server) scrobble(ctx context.Context, acct account, event *pluginv1.WatchSyncEvent) (*pluginv1.WatchSyncApplyResult, *pluginv1.WatchSyncFault) {
	var path string
	switch event.GetOperation() {
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START:
		path = "/scrobble/start"
	case pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE:
		path = "/scrobble/pause"
	default:
		path = "/scrobble/stop"
		if acct.trackRewatches && event.GetCompleted() {
			path += "?allow_rewatch=yes"
		}
	}
	log := s.scrobbleLogger(event)
	payload, fault := buildScrobblePayload(event)
	if fault != nil {
		log.Warn("Simkl scrobble not sent", "reason", fault.GetSafeMessage())
		return rejected(event.GetEventId(), fault), nil
	}
	status, fault := s.simkl.post(ctx, acct, path, payload, nil)
	if status == http.StatusConflict {
		if event.GetOperation() == pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP && event.GetCompleted() {
			log.Info("Simkl scrobble already recorded", statusField(status)...)
			return noChange(event.GetEventId()), nil
		}
		log.Warn("Simkl scrobble refused as a conflict", statusField(status)...)
		return rejected(event.GetEventId(), invalidRequestFault("Simkl refused the playback event as a conflict (HTTP 409)")), nil
	}
	if fault != nil {
		message := "Simkl scrobble failed"
		if status == http.StatusNotFound {
			message = "Simkl scrobble failed: Simkl could not match the item"
		}
		log.Warn(message, append(statusField(status), "error", fault.GetSafeMessage())...)
		return nil, fault
	}
	log.Info("Simkl scrobble sent", statusField(status)...)
	return applied(event.GetEventId()), nil
}

// scrobbleLogger describes a playback event in the fields an admin needs to
// find it in Silo and see what Simkl was asked to match. It names the IDs and
// whether a title went along, never the title itself or any credential. The
// IDs still identify what was watched, so the log records viewing activity.
func (s *Server) scrobbleLogger(event *pluginv1.WatchSyncEvent) hclog.Logger {
	media := event.GetMedia()
	action := strings.ToLower(strings.TrimPrefix(event.GetOperation().String(), "WATCH_SYNC_OPERATION_SCROBBLE_"))
	kind := strings.ToLower(strings.TrimPrefix(media.GetMediaType().String(), "WATCH_SYNC_MEDIA_TYPE_"))
	fields := []any{
		"action", action,
		"kind", kind,
		"playback_session_id", event.GetPlaybackSessionId(),
		"media_item_id", media.GetMediaItemId(),
		"ids", externalIDsField(media.GetExternalIds()),
		"completed", event.GetCompleted(),
	}
	if media.GetMediaType() == pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE {
		fields = append(fields,
			"show_ids", externalIDsField(media.GetSeriesExternalIds()),
			"season", media.GetSeasonNumber(),
			"episode", media.GetEpisodeNumber(),
			"show_title_sent", strings.TrimSpace(media.GetSeriesTitle()) != "",
		)
	} else {
		fields = append(fields, "title_sent", strings.TrimSpace(media.GetTitle()) != "")
	}
	return s.log.With(fields...)
}

// statusField is the HTTP status as a log field, or nothing when no response
// arrived, such as after a network error.
func statusField(status int) []any {
	if status == 0 {
		return nil
	}
	return []any{"status", status}
}

// externalIDsField renders the IDs Simkl is sent, in a stable order, such as
// "imdb:tt0903747 tvdb:81189", or "none".
func externalIDsField(ids map[string]string) string {
	var parts []string
	for _, namespace := range []string{"imdb", "tmdb", "tvdb"} {
		if value := strings.TrimSpace(ids[namespace]); value != "" {
			parts = append(parts, namespace+":"+value)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

func buildScrobblePayload(event *pluginv1.WatchSyncEvent) (map[string]any, *pluginv1.WatchSyncFault) {
	media := event.GetMedia()
	progress := 0.0
	if event.GetDurationSeconds() > 0 {
		progress = event.GetPositionSeconds() / event.GetDurationSeconds() * 100
	}
	payload := map[string]any{"progress": progress}
	ids := media.GetExternalIds()
	switch media.GetMediaType() {
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE:
		series := media.GetSeriesExternalIds()
		payload["show"] = map[string]any{"ids": idsFromLocal(series["imdb"], series["tmdb"], series["tvdb"])}
		payload["episode"] = map[string]any{
			"season": int(media.GetSeasonNumber()),
			"number": int(media.GetEpisodeNumber()),
			"ids":    idsFromLocal(ids["imdb"], ids["tmdb"], ids["tvdb"]),
		}
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:
		payload["movie"] = map[string]any{"ids": idsFromLocal(ids["imdb"], ids["tmdb"], ids["tvdb"])}
	default:
		return nil, invalidRequestFault("Simkl scrobbles movies and episodes only")
	}
	return payload, nil
}
