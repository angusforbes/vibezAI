package cdp

import (
	"encoding/json"
	"fmt"

	"github.com/simone-vibes/vibez/internal/audioquality"
	"github.com/simone-vibes/vibez/internal/player"
)

// buildSetQueueJS returns the JS expression that calls vibezSetQueue.
// The IDs are JSON-encoded and passed as a JSON string literal so the
// browser side can JSON.parse() them without a second round-trip.
func buildSetQueueJS(ids []string) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal queue ids: %w", err)
	}
	js, err := json.Marshal(string(b))
	if err != nil {
		return "", fmt.Errorf("cdp: marshal queue json string: %w", err)
	}
	return fmt.Sprintf(`window.vibezSetQueue && window.vibezSetQueue(%s)`, js), nil
}

// buildSetQueueAtJS returns the JS expression that calls vibezSetQueue with a
// start ID, so playback begins at that entry instead of the first.
func buildSetQueueAtJS(ids []string, startID string) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal queue ids: %w", err)
	}
	js, err := json.Marshal(string(b))
	if err != nil {
		return "", fmt.Errorf("cdp: marshal queue json string: %w", err)
	}
	start, err := json.Marshal(startID)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal start id: %w", err)
	}
	return fmt.Sprintf(`window.vibezSetQueue && window.vibezSetQueue(%s,%s)`, js, start), nil
}

// buildSyncQueueJS returns the JS expression that calls vibezSyncQueue.
func buildSyncQueueJS(ids []string, currentID, playID string) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal queue ids: %w", err)
	}
	js, err := json.Marshal(string(b))
	if err != nil {
		return "", fmt.Errorf("cdp: marshal queue json string: %w", err)
	}
	cur, err := json.Marshal(currentID)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal current id: %w", err)
	}
	play, err := json.Marshal(playID)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal play id: %w", err)
	}
	return fmt.Sprintf(`window.vibezSyncQueue && window.vibezSyncQueue(%s,%s,%s)`, js, cur, play), nil
}

// buildPlayQueuedJS returns the JS expression that calls vibezPlayQueued.
func buildPlayQueuedJS(idx int, id string) (string, error) {
	js, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal track id: %w", err)
	}
	return fmt.Sprintf(`window.vibezPlayQueued && window.vibezPlayQueued(%d,%s)`, idx, js), nil
}

// buildSetPlaylistJS returns the JS expression that calls vibezSetPlaylist.
func buildSetPlaylistJS(playlistID string, startIdx int) (string, error) {
	js, err := json.Marshal(playlistID)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal playlist id: %w", err)
	}
	return fmt.Sprintf(`window.vibezSetPlaylist && window.vibezSetPlaylist(%s,%d)`, js, startIdx), nil
}

// buildAppendQueueJS returns the JS expression that calls vibezAppendQueue.
func buildAppendQueueJS(ids []string) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal append ids: %w", err)
	}
	js, err := json.Marshal(string(b))
	if err != nil {
		return "", fmt.Errorf("cdp: marshal append json string: %w", err)
	}
	return fmt.Sprintf(`window.vibezAppendQueue && window.vibezAppendQueue(%s)`, js), nil
}

func buildSetAudioBitrateJS(kbps int) (string, error) {
	if err := audioquality.Validate(kbps); err != nil {
		return "", err
	}
	return fmt.Sprintf(`window.vibezSetAudioBitrate && window.vibezSetAudioBitrate(%d)`, kbps), nil
}

func buildSetEqualizerJS(bands []player.EQBand) (string, error) {
	b, err := json.Marshal(bands)
	if err != nil {
		return "", fmt.Errorf("cdp: marshal eq bands: %w", err)
	}
	js, err := json.Marshal(string(b))
	if err != nil {
		return "", fmt.Errorf("cdp: marshal eq json string: %w", err)
	}
	return fmt.Sprintf(`window.vibezSetEqualizer && window.vibezSetEqualizer(%s)`, js), nil
}
