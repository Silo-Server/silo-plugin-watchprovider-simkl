package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/hashicorp/go-hclog"
)

// captureLogs points server's logger at a buffer in the JSON format Silo's
// plugin host reads, and returns the buffer.
func captureLogs(server *Server) *bytes.Buffer {
	var buffer bytes.Buffer
	server.SetLogger(hclog.New(&hclog.LoggerOptions{Level: hclog.Info, Output: &buffer, JSONFormat: true}))
	return &buffer
}

func logEntries(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buffer.Bytes()))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("log line %q is not JSON: %v", scanner.Text(), err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func titledEpisodeStop() *pluginv1.WatchSyncEvent {
	return &pluginv1.WatchSyncEvent{
		EventId:           "stop",
		Operation:         pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
		PlaybackSessionId: "session-1",
		PositionSeconds:   2700,
		DurationSeconds:   2700,
		Completed:         true,
		Media: &pluginv1.WatchSyncMedia{
			MediaItemId:       "item-1",
			MediaType:         mediaEpisode,
			Title:             "Pilot",
			ExternalIds:       map[string]string{"tmdb": "900001"},
			SeriesTitle:       "Example Show",
			SeriesExternalIds: map[string]string{"tmdb": "900002"},
			SeasonNumber:      1,
			EpisodeNumber:     1,
		},
	}
}

func TestScrobbleLogsAnUnmatchedItemAsAWarning(t *testing.T) {
	server, _ := recordingServer(t, http.StatusNotFound, `{"error":"id_err"}`)
	logs := captureLogs(server)
	applyEvents(t, server, titledEpisodeStop())

	entries := logEntries(t, logs)
	if len(entries) != 1 {
		t.Fatalf("log entries = %v, want one", entries)
	}
	entry := entries[0]
	want := map[string]any{
		"@level":              "warn",
		"action":              "stop",
		"kind":                "episode",
		"playback_session_id": "session-1",
		"media_item_id":       "item-1",
		"ids":                 "tmdb:900001",
		"show_ids":            "tmdb:900002",
		"season":              float64(1),
		"episode":             float64(1),
		"show_title_sent":     true,
		"completed":           true,
		"status":              float64(404),
	}
	for key, value := range want {
		if entry[key] != value {
			t.Errorf("%s = %#v, want %#v", key, entry[key], value)
		}
	}
	if entry["@message"] != "Simkl scrobble failed: Simkl could not match the item" {
		t.Errorf("message = %q", entry["@message"])
	}
	assertLogsCarryNoSecretsOrTitles(t, logs)
}

func TestScrobbleLogsEachOutcomeOnce(t *testing.T) {
	unfinished := titledEpisodeStop()
	unfinished.Completed = false
	series := scrobbleStart()
	series.Media.MediaType = mediaSeries
	for _, tc := range []struct {
		name        string
		status      int // 0 drops the connection without a response
		event       *pluginv1.WatchSyncEvent
		wantLevel   string
		wantMessage string
		wantStatus  any
	}{
		{name: "accepted", status: http.StatusCreated, event: scrobbleStart(), wantLevel: "info", wantMessage: "Simkl scrobble sent", wantStatus: float64(201)},
		{name: "finished stop already recorded", status: http.StatusConflict, event: titledEpisodeStop(), wantLevel: "info", wantMessage: "Simkl scrobble already recorded", wantStatus: float64(409)},
		{name: "unfinished stop conflict", status: http.StatusConflict, event: unfinished, wantLevel: "warn", wantMessage: "Simkl scrobble refused as a conflict", wantStatus: float64(409)},
		{name: "server error", status: http.StatusServiceUnavailable, event: scrobbleStart(), wantLevel: "warn", wantMessage: "Simkl scrobble failed", wantStatus: float64(503)},
		{name: "no response", status: 0, event: scrobbleStart(), wantLevel: "warn", wantMessage: "Simkl scrobble failed", wantStatus: nil},
		{name: "unsupported item", status: http.StatusCreated, event: series, wantLevel: "warn", wantMessage: "Simkl scrobble not sent", wantStatus: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{}`))
			}))
			logs := captureLogs(server)
			applyEvents(t, server, tc.event)

			entries := logEntries(t, logs)
			if len(entries) != 1 {
				t.Fatalf("log entries = %v, want one", entries)
			}
			entry := entries[0]
			if entry["@level"] != tc.wantLevel || entry["@message"] != tc.wantMessage || entry["status"] != tc.wantStatus {
				t.Fatalf("entry = %v, want %s %q status %v", entry, tc.wantLevel, tc.wantMessage, tc.wantStatus)
			}
		})
	}
}

func assertLogsCarryNoSecretsOrTitles(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	for _, forbidden := range []string{testToken, testClientID, "Example Show", "Pilot"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("logs contain %q: %s", forbidden, logs.String())
		}
	}
}
