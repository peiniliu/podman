package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/containers/image/directory"
	dockerarchive "github.com/containers/image/docker/archive"
	"github.com/containers/image/docker/reference"
	"github.com/containers/image/manifest"
	ociarchive "github.com/containers/image/oci/archive"
	"github.com/containers/image/types"
	"github.com/containers/libpod/cmd/podman/cliconfig"
	"github.com/containers/libpod/cmd/podman/libpodruntime"
	libpodImage "github.com/containers/libpod/libpod/image"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	ociManifestDir  = "oci-dir"
	v2s2ManifestDir = "docker-dir"
)

var (
	saveCommand     cliconfig.SaveValues
	saveDescription = `
	Save an image to docker-archive or oci-archive on the local machine.
	Default is docker-archive`

	_saveCommand = &cobra.Command{
		Use:   "save",
		Short: "Save image to an archive",
		Long:  saveDescription,
		RunE: func(cmd *cobra.Command, args []string) error {
			saveCommand.InputArgs = args
			saveCommand.GlobalFlags = MainGlobalOpts
			return saveCmd(&saveCommand)
		},
		Example: "",
	}
)

func init() {
	saveCommand.Command = _saveCommand
	flags := saveCommand.Flags()
	flags.BoolVar(&saveCommand.Compress, "compress", false, "Compress tarball image layers when saving to a directory using the 'dir' transport. (default is same compression type as source)")
	flags.StringVar(&saveCommand.Format, "format", "", "Save image to oci-archive, oci-dir (directory with oci manifest type), docker-dir (directory with v2s2 manifest type)")
	flags.StringVarP(&saveCommand.Output, "output", "o", "/dev/stdout", "Write to a file, default is STDOUT")
	flags.BoolVarP(&saveCommand.Quiet, "quiet", "q", false, "Suppress the output")
}

// saveCmd saves the image to either docker-archive or oci
func saveCmd(c *cliconfig.SaveValues) error {
	args := c.InputArgs
	if len(args) == 0 {
		return errors.Errorf("need at least 1 argument")
	}

	runtime, err := libpodruntime.GetRuntime(&c.PodmanCommand)
	if err != nil {
		return errors.Wrapf(err, "could not create runtime")
	}
	defer runtime.Shutdown(false)

	if c.Flag("compress").Changed && (c.Format != ociManifestDir && c.Format != v2s2ManifestDir && c.Format == "") {
		return errors.Errorf("--compress can only be set when --format is either 'oci-dir' or 'docker-dir'")
	}

	var writer io.Writer
	if !c.Quiet {
		writer = os.Stderr
	}

	output := c.Output
	if output == "/dev/stdout" {
		fi := os.Stdout
		if logrus.IsTerminal(fi) {
			return errors.Errorf("refusing to save to terminal. Use -o flag or redirect")
		}
	}
	if err := validateFileName(output); err != nil {
		return err
	}

	source := args[0]
	newImage, err := runtime.ImageRuntime().NewFromLocal(source)
	if err != nil {
		return err
	}

	var destRef types.ImageReference
	var manifestType string
	switch c.Format {
	case "oci-archive":
		destImageName := imageNameForSaveDestination(newImage, source)
		destRef, err = ociarchive.NewReference(output, destImageName) // destImageName may be ""
		if err != nil {
			return errors.Wrapf(err, "error getting OCI archive ImageReference for (%q, %q)", output, destImageName)
		}
	case "oci-dir":
		destRef, err = directory.NewReference(output)
		if err != nil {
			return errors.Wrapf(err, "error getting directory ImageReference for %q", output)
		}
		manifestType = imgspecv1.MediaTypeImageManifest
	case "docker-dir":
		destRef, err = directory.NewReference(output)
		if err != nil {
			return errors.Wrapf(err, "error getting directory ImageReference for %q", output)
		}
		manifestType = manifest.DockerV2Schema2MediaType
	case "docker-archive", "":
		dst := output
		destImageName := imageNameForSaveDestination(newImage, source)
		if destImageName != "" {
			dst = fmt.Sprintf("%s:%s", dst, destImageName)
		}
		destRef, err = dockerarchive.ParseReference(dst) // FIXME? Add dockerarchive.NewReference
		if err != nil {
			return errors.Wrapf(err, "error getting Docker archive ImageReference for %q", dst)
		}
	default:
		return errors.Errorf("unknown format option %q", c.String("format"))
	}

	// supports saving multiple tags to the same tar archive
	var additionaltags []reference.NamedTagged
	if len(args) > 1 {
		additionaltags, err = libpodImage.GetAdditionalTags(args[1:])
		if err != nil {
			return err
		}
	}
	if err := newImage.PushImageToReference(getContext(), destRef, manifestType, "", "", writer, c.Bool("compress"), libpodImage.SigningOptions{}, &libpodImage.DockerRegistryOptions{}, additionaltags); err != nil {
		if err2 := os.Remove(output); err2 != nil {
			logrus.Errorf("error deleting %q: %v", output, err)
		}
		return errors.Wrapf(err, "unable to save %q", args)
	}

	return nil
}

// imageNameForSaveDestination returns a Docker-like reference appropriate for saving img,
// which the user referred to as imgUserInput; or an empty string, if there is no appropriate
// reference.
func imageNameForSaveDestination(img *libpodImage.Image, imgUserInput string) string {
	if strings.Contains(img.ID(), imgUserInput) {
		return ""
	}

	prepend := ""
	localRegistryPrefix := fmt.Sprintf("%s/", libpodImage.DefaultLocalRegistry)
	if !strings.HasPrefix(imgUserInput, localRegistryPrefix) {
		// we need to check if localhost was added to the image name in NewFromLocal
		for _, name := range img.Names() {
			// If the user is saving an image in the localhost registry,  getLocalImage need
			// a name that matches the format localhost/<tag1>:<tag2> or localhost/<tag>:latest to correctly
			// set up the manifest and save.
			if strings.HasPrefix(name, localRegistryPrefix) && (strings.HasSuffix(name, imgUserInput) || strings.HasSuffix(name, fmt.Sprintf("%s:latest", imgUserInput))) {
				prepend = localRegistryPrefix
				break
			}
		}
	}
	return fmt.Sprintf("%s%s", prepend, imgUserInput)
}
