package player

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ygelfand/plexctl/internal/config"
	"github.com/ygelfand/plexctl/internal/plex"
	"github.com/ygelfand/plexctl/internal/ui"
)

func PlayMedia(metadata *components.Metadata, noReport bool, tctMode bool, startOffset int64) tea.Cmd {
	if metadata == nil || len(metadata.Media) == 0 || len(metadata.Media[0].Part) == 0 {
		return func() tea.Msg { return fmt.Errorf("no playable media found") }
	}

	cfg := config.Get()
	_, serverCfg, ok := cfg.GetActiveServer()
	if !ok {
		return func() tea.Msg { return fmt.Errorf("no active server") }
	}

	part := metadata.Media[0].Part[0]
	separator := "?"
	if strings.Contains(part.Key, "?") {
		separator = "&"
	}
	playURL := fmt.Sprintf("%s%s%sX-Plex-Token=%s", serverCfg.URL, part.Key, separator, cfg.Token)

	var subtitles []ExternalSubtitle
	for _, stream := range part.Stream {
		if stream.StreamType == components.StreamTypeSubtitle && stream.Key != "" {
			sep := "?"
			if strings.Contains(stream.Key, "?") {
				sep = "&"
			}
			subURL := fmt.Sprintf("%s%s%sX-Plex-Token=%s", serverCfg.URL, stream.Key, sep, cfg.Token)
			lang := ""
			if stream.LanguageCode != nil {
				lang = *stream.LanguageCode
			}
			subtitles = append(subtitles, ExternalSubtitle{
				URL:      subURL,
				Title:    stream.DisplayTitle,
				Language: lang,
			})
		}
	}

	slog.Debug("Playing media", "url", playURL, "tct", tctMode, "offset", startOffset, "subtitles", len(subtitles))
	rk := ""
	if metadata.RatingKey != nil {
		rk = *metadata.RatingKey
	}

	title := metadata.Title
	if noReport {
		title = "Trailer: " + title
	}

	return GetPlayerManager().Play(playURL, title, rk, noReport, tctMode, startOffset, subtitles)
}

// buildQueueItem turns a fully-fetched metadata into a QueueItem with subtitle URLs.
func buildQueueItem(metadata *components.Metadata, noReport bool) (QueueItem, error) {
	if metadata == nil || len(metadata.Media) == 0 || len(metadata.Media[0].Part) == 0 {
		return QueueItem{}, fmt.Errorf("no playable media for %q", metadata.Title)
	}
	cfg := config.Get()
	_, serverCfg, ok := cfg.GetActiveServer()
	if !ok {
		return QueueItem{}, fmt.Errorf("no active server")
	}

	part := metadata.Media[0].Part[0]
	separator := "?"
	if strings.Contains(part.Key, "?") {
		separator = "&"
	}
	playURL := fmt.Sprintf("%s%s%sX-Plex-Token=%s", serverCfg.URL, part.Key, separator, cfg.Token)

	var subtitles []ExternalSubtitle
	for _, stream := range part.Stream {
		if stream.StreamType == components.StreamTypeSubtitle && stream.Key != "" {
			sep := "?"
			if strings.Contains(stream.Key, "?") {
				sep = "&"
			}
			subURL := fmt.Sprintf("%s%s%sX-Plex-Token=%s", serverCfg.URL, stream.Key, sep, cfg.Token)
			lang := ""
			if stream.LanguageCode != nil {
				lang = *stream.LanguageCode
			}
			subtitles = append(subtitles, ExternalSubtitle{
				URL:      subURL,
				Title:    stream.DisplayTitle,
				Language: lang,
			})
		}
	}

	rk := ""
	if metadata.RatingKey != nil {
		rk = *metadata.RatingKey
	}

	return QueueItem{
		URL:       playURL,
		Title:     metadata.Title,
		RatingKey: rk,
		NoReport:  noReport,
		Subtitles: subtitles,
	}, nil
}

// FetchAndPlayQueue fetches full metadata for each rating key and plays them as a queue.
func FetchAndPlayQueue(ratingKeys []string, tctMode bool) tea.Cmd {
	return func() tea.Msg {
		if len(ratingKeys) == 0 {
			return fmt.Errorf("no items to play")
		}

		var items []QueueItem
		for _, rk := range ratingKeys {
			meta, err := plex.GetMetadata(context.Background(), rk, true)
			if err != nil {
				slog.Warn("FetchAndPlayQueue: failed to fetch metadata", "ratingKey", rk, "error", err)
				continue
			}
			item, err := buildQueueItem(meta, false)
			if err != nil {
				slog.Warn("FetchAndPlayQueue: skipping unplayable item", "ratingKey", rk, "error", err)
				continue
			}
			items = append(items, item)
		}

		if len(items) == 0 {
			return fmt.Errorf("no playable items in queue")
		}

		slog.Debug("FetchAndPlayQueue: dispatching queue", "count", len(items))
		cmd := GetPlayerManager().PlayQueue(items, tctMode, 0)
		if cmd != nil {
			return cmd()
		}
		return nil
	}
}

// FetchAndPlay handles the full playback logic: fetch full metadata, check for resume, then play
func FetchAndPlay(ratingKey string, tctMode bool) tea.Cmd {
	return func() tea.Msg {
		meta, err := plex.GetMetadata(context.Background(), ratingKey, true)
		if err != nil {
			return err
		}

		if meta.ViewOffset != nil && *meta.ViewOffset > 0 {
			return ui.ResumeChoiceMsg{
				Metadata: meta,
				TctMode:  tctMode,
			}
		}

		cmd := PlayMedia(meta, false, tctMode, 0)
		if cmd != nil {
			return cmd()
		}
		return nil
	}
}

// FetchAndPlayTrailer handles finding and playing the correct trailer for a media item
func FetchAndPlayTrailer(parentMeta *components.Metadata, tctMode bool) tea.Cmd {
	return func() tea.Msg {
		targetKey := ""
		if parentMeta.PrimaryExtraKey != nil {
			targetKey = *parentMeta.PrimaryExtraKey
		} else if parentMeta.Extras != nil {
			for _, extra := range parentMeta.Extras.Metadata {
				if extra.Subtype != nil && *extra.Subtype == "trailer" {
					if extra.RatingKey != nil {
						targetKey = *extra.RatingKey
						break
					}
				}
			}
		}

		if targetKey == "" {
			return fmt.Errorf("no trailer found for this item")
		}

		// Normalize key (some extra keys are full paths)
		parts := strings.Split(targetKey, "/")
		ratingKey := parts[len(parts)-1]

		slog.Debug("FetchAndPlayTrailer: Resolved trailer key", "parent", parentMeta.Title, "target", ratingKey)

		meta, err := plex.GetMetadata(context.Background(), ratingKey, false)
		if err != nil {
			return err
		}

		cmd := PlayMedia(meta, true, tctMode, 0)
		if cmd != nil {
			return cmd()
		}
		return nil
	}
}
