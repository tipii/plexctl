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
// TUI. For Kitty, this also persists the downscaled PNG bytes to disk so
// later renders can skip decode/resize/encode.
//
// For halfcell, returns the go-pixels output (rows is ignored; go-pixels
// computes height from the image aspect ratio).
func RenderPoster(img image.Image, cols, rows int, ratingKey string, proto Protocol) (string, error) {
	switch proto {
	case ProtocolKitty:
		pngBytes, err := EncodeKittyPNG(img, cols, rows)
		if err != nil {
			return "", err
		}
		if ratingKey != "" {
			plex.SetCachedKittyPNG(ratingKey, cols, rows, pngBytes)
		}
		return renderKittyFromPNG(pngBytes, ratingKey, cols, rows), nil
	default:
		return gopixels.FromImageStream(img, cols, 0, "halfcell", true)
	}
}

// RenderPosterCached returns a renderable poster string from the disk cache
// without touching the source image, or ok=false on miss. For Kitty, this
// triggers a transmit if the image id hasn't been sent yet in this process.
func RenderPosterCached(ratingKey string, cols int, proto Protocol) (string, bool) {
	switch proto {
	case ProtocolHalfcell:
		return plex.GetCachedPoster(ratingKey, cols)
	case ProtocolKitty:
		pngBytes, rows, ok := plex.GetCachedKittyPNG(ratingKey, cols)
		if !ok {
			return "", false
		}
		return renderKittyFromPNG(pngBytes, ratingKey, cols, rows), true
	}
	return "", false
}

// renderKittyFromPNG handles the per-process transmit bookkeeping for an
// already-encoded PNG and returns the placeholder grid that references it.
func renderKittyFromPNG(pngBytes []byte, ratingKey string, cols, rows int) string {
	id := KittyImageID(ratingKey, cols, rows)
	if MarkKittyTransmitted(id) {
		seq := TransmitKittyFromPNG(pngBytes, cols, rows, id)
		kittyStdoutMu.Lock()
		_, _ = os.Stdout.WriteString(WrapTmuxPassthrough(seq))
		kittyStdoutMu.Unlock()
	}
	return KittyPlaceholderGrid(id, cols, rows)
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
