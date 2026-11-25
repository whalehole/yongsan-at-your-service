package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

type imagePrefixFlags struct {
	value string
	skip  bool
}

func (p *imagePrefixFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.value, "image-prefix", "", "Prefix output image names with a registry/namespace path")
	cmd.Flags().BoolVar(&p.skip, "no-image-prefix", false, "Keep image names unchanged and skip prefix selection")
}

func (a *app) chooseImagePrefix(cmd *cobra.Command, prefixes []string, flags imagePrefixFlags, prompt bool) (string, error) {
	if changed(cmd, "image-prefix") {
		if flags.skip {
			return "", fmt.Errorf("--image-prefix and --no-image-prefix cannot be used together")
		}
		if err := config.ValidateImagePrefix(flags.value); err != nil {
			return "", err
		}
		return strings.TrimSuffix(flags.value, "/"), nil
	}
	if flags.skip || !prompt || !a.prompt.interactive() || len(prefixes) == 0 {
		return "", nil
	}
	choices := append(append([]string(nil), prefixes...), "Skip (keep image names unchanged)")
	choice, err := a.prompt.choose(cmd.Context(), "Select an output image prefix:", choices)
	if err != nil {
		return "", err
	}
	if choice == len(prefixes) {
		return "", nil
	}
	return strings.TrimSuffix(prefixes[choice], "/"), nil
}

func prefixedImage(prefix, image string) (string, error) {
	if err := config.ValidateImage(image); err != nil {
		return "", err
	}
	if prefix == "" || strings.HasPrefix(image, prefix+"/") {
		return image, nil
	}
	parts := strings.Split(image, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		return "", fmt.Errorf("image %q already specifies a different registry; choose Skip/--no-image-prefix or use a relative image name", image)
	}
	full := prefix + "/" + image
	if err := config.ValidateImage(full); err != nil {
		return "", err
	}
	return full, nil
}
