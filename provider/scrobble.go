package provider

import (
	"context"
	"net/http"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
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
	payload, fault := buildScrobblePayload(event)
	if fault != nil {
		return rejected(event.GetEventId(), fault), nil
	}
	status, fault := s.simkl.post(ctx, acct, path, payload, nil)
	if status == http.StatusConflict {
		if event.GetOperation() == pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP && event.GetCompleted() {
			return noChange(event.GetEventId()), nil
		}
		return rejected(event.GetEventId(), invalidRequestFault("Simkl refused the playback event as a conflict (HTTP 409)")), nil
	}
	if fault != nil {
		return nil, fault
	}
	return applied(event.GetEventId()), nil
}

// buildScrobblePayload identifies the item the way Simkl's scrobble guide
// asks: every ID Silo has, plus the title and year. Simkl walks the IDs first
// and falls back to title and year when none resolves, so an item whose IDs
// Simkl does not map still scrobbles instead of failing with 404 id_err.
//
// An episode carries its show, its season and episode number, and its own IDs
// when it has any, as the Jellyfin Simkl plugin sends it.
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
		payload["show"] = scrobbleItem(media.GetSeriesTitle(), media.GetSeriesYear(), idsFromLocal(series["imdb"], series["tmdb"], series["tvdb"]))
		episode := map[string]any{
			"season": int(media.GetSeasonNumber()),
			"number": int(media.GetEpisodeNumber()),
		}
		if episodeIDs := idsFromLocal(ids["imdb"], ids["tmdb"], ids["tvdb"]); episodeIDs != (simklIDs{}) {
			episode["ids"] = episodeIDs
		}
		payload["episode"] = episode
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:
		payload["movie"] = scrobbleItem(media.GetTitle(), media.GetYear(), idsFromLocal(ids["imdb"], ids["tmdb"], ids["tvdb"]))
	default:
		return nil, invalidRequestFault("Simkl scrobbles movies and episodes only")
	}
	return payload, nil
}

// scrobbleItem is a movie or show object. Empty fields are left out, so an
// item without usable IDs is matched on its title and year alone. The title
// goes only with a year: without one, Simkl falls back to matching the title
// alone, which can credit a different title of the same name without saying
// so, and a 404 is the safer outcome.
func scrobbleItem(title string, year int32, ids simklIDs) map[string]any {
	item := map[string]any{}
	if title = strings.TrimSpace(title); title != "" && year > 0 {
		item["title"] = title
		item["year"] = int(year)
	}
	if ids != (simklIDs{}) {
		item["ids"] = ids
	}
	return item
}
