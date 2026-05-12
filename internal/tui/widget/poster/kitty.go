package poster

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"os"
	"strings"
	"sync"

	"golang.org/x/image/draw"
)

// WrapTmuxPassthrough wraps a control sequence in tmux's DCS passthrough
// envelope when $TMUX is set, doubling every ESC inside the payload as the
// tmux protocol requires. Returns s unchanged outside tmux.
//
// The host tmux session needs `set -g allow-passthrough on` for the wrapped
// sequence to reach the outer terminal.
func WrapTmuxPassthrough(s string) string {
	if os.Getenv("TMUX") == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s)*2 + 16)
	b.WriteString("\x1bPtmux;")
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b {
			b.WriteByte(0x1b)
			b.WriteByte(0x1b)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteString("\x1b\\")
	return b.String()
}

// kittyPixelPerCellX, kittyPixelPerCellY are conservative pixel-per-cell
// estimates for downscaling. The Kitty terminal will resample anyway, so
// hitting the exact display resolution isn't required — we just want a
// source small enough that PNG encoding isn't the bottleneck.
const (
	kittyPixelPerCellX = 20
	kittyPixelPerCellY = 40
)

// downscaleForKitty returns a copy of img sized to approximately the target
// display footprint, or img unchanged if it's already smaller. Catmull-Rom
// gives a decent quality/speed trade-off for posters.
func downscaleForKitty(img image.Image, cols, rows int) image.Image {
	targetW := cols * kittyPixelPerCellX
	targetH := rows * kittyPixelPerCellY

	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= targetW && srcH <= targetH {
		return img
	}

	// Scale by the more constrained axis to preserve aspect.
	scale := float64(targetW) / float64(srcW)
	if float64(srcH)*scale > float64(targetH) {
		scale = float64(targetH) / float64(srcH)
	}
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// EncodeKittyInline returns a Kitty graphics escape that transmits the image
// as PNG and displays it inline (a=T) at (cols, rows). Suitable for direct
// stdout printing OUTSIDE Bubble Tea. Inside a TUI, use TransmitKitty +
// KittyPlaceholderGrid instead.
func EncodeKittyInline(img image.Image, cols, rows int) (string, error) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return "", fmt.Errorf("png encode: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	const chunkSize = 4096
	var out strings.Builder
	for i := 0; i < len(b64); i += chunkSize {
		end := i + chunkSize
		if end > len(b64) {
			end = len(b64)
		}
		chunk := b64[i:end]
		isLast := end == len(b64)
		isFirst := i == 0

		out.WriteString("\x1b_G")
		if isFirst {
			fmt.Fprintf(&out, "f=100,a=T,c=%d,r=%d,q=2,", cols, rows)
		}
		if isLast {
			out.WriteString("m=0")
		} else {
			out.WriteString("m=1")
		}
		out.WriteString(";")
		out.WriteString(chunk)
		out.WriteString("\x1b\\")
	}
	return out.String(), nil
}

// TransmitKitty returns the Kitty graphics sequence that uploads the image
// under image id `id` without rendering it. The image is registered with
// U=1 (Unicode placeholders) so it can be placed via KittyPlaceholderGrid.
//
// Emit this once per (id, image content). Kitty stores images by id; repeat
// transmits are harmless but waste bandwidth.
func TransmitKitty(img image.Image, cols, rows int, id uint32) (string, error) {
	pngBytes, err := EncodeKittyPNG(img, cols, rows)
	if err != nil {
		return "", err
	}
	return TransmitKittyFromPNG(pngBytes, cols, rows, id), nil
}

// EncodeKittyPNG downscales img to the target cell footprint and returns the
// PNG bytes that would be sent to the terminal. Callers can persist these
// bytes to skip the resize+encode work on later transmits.
func EncodeKittyPNG(img image.Image, cols, rows int) ([]byte, error) {
	scaled := downscaleForKitty(img, cols, rows)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, scaled); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return pngBuf.Bytes(), nil
}

// TransmitKittyFromPNG builds the Kitty graphics sequence from pre-encoded
// PNG bytes. Mirrors TransmitKitty but skips the downscale + PNG encode.
func TransmitKittyFromPNG(pngBytes []byte, cols, rows int, id uint32) string {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	const chunkSize = 4096
	var out strings.Builder
	for i := 0; i < len(b64); i += chunkSize {
		end := i + chunkSize
		if end > len(b64) {
			end = len(b64)
		}
		chunk := b64[i:end]
		isLast := end == len(b64)
		isFirst := i == 0

		out.WriteString("\x1b_G")
		if isFirst {
			// a=T + U=1 transmits the image AND creates the virtual placement
			// that Unicode placeholder cells will later resolve against.
			// c,r set the placement's cell dimensions.
			fmt.Fprintf(&out, "f=100,a=T,U=1,i=%d,c=%d,r=%d,q=2,", id, cols, rows)
		}
		if isLast {
			out.WriteString("m=0")
		} else {
			out.WriteString("m=1")
		}
		out.WriteString(";")
		out.WriteString(chunk)
		out.WriteString("\x1b\\")
	}
	return out.String()
}

// KittyPlaceholderGrid returns a (cols × rows) block of Unicode placeholder
// cells referencing the image previously transmitted as `id`. The block ends
// without a trailing newline; lipgloss can lay it out as a normal string.
//
// Each cell encodes:
//   - 24-bit foreground color = image id (R=byte0, G=byte1, B=byte2, MSB first)
//   - the placeholder character U+10EEEE
//   - a row diacritic and a column diacritic
//
// The image id must fit in 24 bits (max 0xFFFFFF). Rows/cols up to
// len(kittyDiacritics)-1 are supported.
func KittyPlaceholderGrid(id uint32, cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	if cols > len(kittyDiacritics) {
		cols = len(kittyDiacritics)
	}
	if rows > len(kittyDiacritics) {
		rows = len(kittyDiacritics)
	}

	r := byte((id >> 16) & 0xff)
	g := byte((id >> 8) & 0xff)
	b := byte(id & 0xff)

	// Pre-render the SGR prefix once.
	colorPrefix := fmt.Sprintf("\x1b[38:2:%d:%d:%dm", r, g, b)
	const placeholder = "\U0010EEEE"
	const reset = "\x1b[39m"

	var out strings.Builder
	out.Grow((cols*8 + 16) * rows)

	for row := 0; row < rows; row++ {
		out.WriteString(colorPrefix)
		rowDiac := string(kittyDiacritics[row])
		for col := 0; col < cols; col++ {
			out.WriteString(placeholder)
			out.WriteString(rowDiac)
			out.WriteString(string(kittyDiacritics[col]))
		}
		out.WriteString(reset)
		if row < rows-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// KittyImageID derives a stable 24-bit image id from a (rating key, cols,
// rows) tuple. Returns a non-zero id (0 is reserved by the Kitty spec).
func KittyImageID(ratingKey string, cols, rows int) uint32 {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s|%dx%d", ratingKey, cols, rows)
	id := h.Sum32() & 0x00FFFFFF
	if id == 0 {
		id = 1
	}
	return id
}

// transmittedKittyIDs tracks which image ids have already been uploaded to
// the terminal in this process. Once an id is in the set, callers should
// emit only the placeholder grid, not another transmit.
var transmittedKittyIDs sync.Map // map[uint32]struct{}

// MarkKittyTransmitted records that id has been uploaded. Returns true on
// the first call for a given id, false on subsequent calls (already sent).
func MarkKittyTransmitted(id uint32) (firstTime bool) {
	_, loaded := transmittedKittyIDs.LoadOrStore(id, struct{}{})
	return !loaded
}

// kittyDiacritics is the canonical Kitty unicode-placeholder diacritic table
// (subset). Index i encodes row/column i. The full table is 297 entries;
// we ship the first 64, which is plenty for posters and detail views.
var kittyDiacritics = [...]rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
}
