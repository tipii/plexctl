package poster

import (
	"image"
	"os"
	"strings"
	"sync"

	gopixels "github.com/saran13raj/go-pixels"
	"github.com/ygelfand/plexctl/internal/config"
	"github.com/ygelfand/plexctl/internal/plex"
)

// kittyStdoutMu serializes direct os.Stdout writes from poster goroutines so
// concurrent Kitty transmits don't interleave inside Bubble Tea's output.
var kittyStdoutMu sync.Mutex

// RenderPoster encodes a decoded image into the cell-grid string used by the
// TUI. For Kitty, the image is transmitted to os.Stdout the first time we see
// its id; the returned string is the placeholder grid that references it.
// For halfcell, returns the go-pixels output (rows is ignored; go-pixels
// computes height from the image aspect ratio).
func RenderPoster(img image.Image, cols, rows int, ratingKey string, proto Protocol) (string, error) {
	switch proto {
	case ProtocolKitty:
		id := KittyImageID(ratingKey, cols, rows)
		if MarkKittyTransmitted(id) {
			seq, err := TransmitKitty(img, cols, rows, id)
			if err != nil {
				return "", err
			}
			kittyStdoutMu.Lock()
			_, _ = os.Stdout.WriteString(seq)
			kittyStdoutMu.Unlock()
		}
		return KittyPlaceholderGrid(id, cols, rows), nil
	default:
		return gopixels.FromImageStream(img, cols, 0, "halfcell", true)
	}
}

// Protocol is the rendering strategy chosen for posters.
type Protocol int

const (
	ProtocolHalfcell Protocol = iota
	ProtocolKitty
	ProtocolOff
)

// ResolveProtocol picks a renderer based on the config override and the
// terminal-capability heuristics. In auto mode, Kitty is selected only when
// the terminal advertises support AND no tmux passthrough hop is in the way.
func ResolveProtocol() Protocol {
	cfg := config.Get()
	switch cfg.ImageProtocol {
	case config.ImageProtocolKitty:
		return ProtocolKitty
	case config.ImageProtocolHalfcell:
		return ProtocolHalfcell
	case config.ImageProtocolOff:
		return ProtocolOff
	}

	// auto / unset
	if os.Getenv("TMUX") != "" {
		// tmux strips the Kitty APC sequence unless passthrough is configured;
		// we haven't shipped passthrough support yet.
		return ProtocolHalfcell
	}
	if terminalSupportsKitty() {
		return ProtocolKitty
	}
	return ProtocolHalfcell
}

// TryCachedPosterStr returns the pre-rendered cached poster string when one
// exists AND the current image protocol can reuse it. Kitty placeholder grids
// are generated fresh per-process (the in-memory transmit-tracking set lives
// only in this process), so kitty bypasses the disk cache.
func TryCachedPosterStr(ratingKey string, width int) (string, bool) {
	if ResolveProtocol() != ProtocolHalfcell {
		return "", false
	}
	return plex.GetCachedPoster(ratingKey, width)
}

// CachePosterStr persists a rendered poster string when the protocol benefits
// from disk caching (halfcell only).
func CachePosterStr(ratingKey string, width int, view string) {
	if ResolveProtocol() != ProtocolHalfcell {
		return
	}
	plex.SetCachedPoster(ratingKey, width, view)
}

func terminalSupportsKitty() bool {
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return true
	}
	if os.Getenv("KONSOLE_VERSION") != "" {
		return true
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return true
	}
	if strings.Contains(os.Getenv("TERM"), "kitty") {
		return true
	}
	return false
}
