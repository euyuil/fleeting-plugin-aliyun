// Command distribution publishes installer-compatible packages with immutable versions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"gitlab.com/gitlab-org/fleeting/fleeting-artifact/pkg/releaser"
	"golang.org/x/mod/semver"
)

func publish(ctx context.Context, ref, dir string) error {
	tag, err := name.NewTag(ref)
	if err != nil {
		return err
	}
	if !validVersion(tag.TagStr()) {
		return errors.New("an explicit immutable semantic version is required")
	}
	_, err = remote.Head(tag, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err == nil {
		return errors.New("version already exists; refusing overwrite")
	}
	var registryErr *transport.Error
	if !errors.As(err, &registryErr) || registryErr.StatusCode != 404 {
		return fmt.Errorf("cannot verify version absence: %w", err)
	}
	_, err = releaser.Release(ctx, ref, releaser.WithDirectory(dir))
	return err
}
func validVersion(version string) bool {
	return semver.IsValid("v"+version) && semver.Canonical("v"+version) == "v"+version
}

func main() {
	validate := flag.String("validate-version", "", "validate an exact semantic version without publishing")
	ref := flag.String("ref", "", "registry/repository:version")
	dir := flag.String("dir", "dist/packages", "package directory")
	flag.Parse()
	if *validate != "" {
		if !validVersion(*validate) {
			fmt.Fprintln(os.Stderr, "an exact semantic version without v or build metadata is required")
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := publish(ctx, *ref, *dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
