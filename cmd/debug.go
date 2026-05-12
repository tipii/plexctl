package cmd

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"

	"github.com/spf13/cobra"
	"github.com/ygelfand/plexctl/internal/commands"
	"github.com/ygelfand/plexctl/internal/plex"
	"github.com/ygelfand/plexctl/internal/tui/widget/poster"
)

var (
	debugPosterCols int
	debugPosterRows int
)

var debugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Internal debugging helpers",
}

var debugPosterCmd = &cobra.Command{
	Use:   "poster [rating_key]",
	Short: "Render an item's poster using the Kitty graphics protocol",
	Args:  cobra.ExactArgs(1),
	RunE: commands.RunWithServer(func(ctx context.Context, client *plex.Client, cmd *cobra.Command, args []string, opts *commands.PlexCtlOptions) error {
		meta, err := plex.GetMetadata(ctx, args[0], false)
		if err != nil {
			return fmt.Errorf("get metadata: %w", err)
		}

		path := ""
		switch {
		case meta.GrandparentThumb != nil:
			path = *meta.GrandparentThumb
		case meta.ParentThumb != nil:
			path = *meta.ParentThumb
		case meta.Thumb != nil:
			path = *meta.Thumb
		}
		if path == "" {
			return fmt.Errorf("no thumb on metadata")
		}

		data, err := plex.GetImage(ctx, path)
		if err != nil {
			return fmt.Errorf("fetch image: %w", err)
		}

		img, format, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("decode image (format=%s): %w", format, err)
		}

		out, err := poster.EncodeKittyInline(img, debugPosterCols, debugPosterRows)
		if err != nil {
			return err
		}

		fmt.Fprint(os.Stdout, out)
		fmt.Fprintln(os.Stdout)
		return nil
	}),
}

func init() {
	debugPosterCmd.Flags().IntVar(&debugPosterCols, "cols", 30, "cells wide")
	debugPosterCmd.Flags().IntVar(&debugPosterRows, "rows", 20, "cells tall")
	debugCmd.AddCommand(debugPosterCmd)
	rootCmd.AddCommand(debugCmd)
}
