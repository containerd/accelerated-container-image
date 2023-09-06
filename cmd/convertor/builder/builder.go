/*
   Copyright The Accelerated Container Image Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package builder

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type Builder interface {
	Build(ctx context.Context) error
}

type BuilderOptions struct {
	Ref       string
	TargetRef string
	Auth      string
	PlainHTTP bool
	WorkDir   string
	OCI       bool
	DB        database.ConversionDatabase
	Engine    BuilderEngineType
}

type overlaybdBuilder struct {
	layers int
	config v1.Image
	engine builderEngine
}

func NewOverlayBDBuilder(ctx context.Context, opt BuilderOptions) (Builder, error) {
	resolver := docker.NewResolver(docker.ResolverOptions{
		Credentials: func(s string) (string, string, error) {
			if i := strings.IndexByte(opt.Auth, ':'); i > 0 {
				return opt.Auth[0:i], opt.Auth[i+1:], nil
			}
			return "", "", nil
		},
		PlainHTTP: opt.PlainHTTP,
	})
	engineBase, err := getBuilderEngineBase(ctx, resolver, opt.Ref, opt.TargetRef)
	if err != nil {
		return nil, err
	}
	engineBase.workDir = opt.WorkDir
	engineBase.oci = opt.OCI
	engineBase.db = opt.DB

	refspec, err := reference.Parse(opt.Ref)
	if err != nil {
		return nil, err
	}
	engineBase.host = refspec.Hostname()
	engineBase.repository = strings.TrimPrefix(refspec.Locator, engineBase.host+"/")

	var engine builderEngine
	switch opt.Engine {
	case Overlaybd:
		engine = NewOverlayBDBuilderEngine(engineBase)
	case TurboOCI:
		engine = NewTurboOCIBuilderEngine(engineBase)
	}
	return &overlaybdBuilder{
		layers: len(engineBase.manifest.Layers),
		engine: engine,
		config: engineBase.config,
	}, nil
}

func (b *overlaybdBuilder) Build(ctx context.Context) error {
	defer b.engine.Cleanup()
	alreadyConverted := make([]chan *v1.Descriptor, b.layers)
	downloaded := make([]chan error, b.layers)
	converted := make([]chan error, b.layers)
	// Errgroups will close the context after wait returns so the operations need their own
	// derived context.
	g, rctx := errgroup.WithContext(ctx)

	for i := 0; i < b.layers; i++ {
		idx := i
		downloaded[idx] = make(chan error)
		converted[idx] = make(chan error)
		alreadyConverted[idx] = make(chan *v1.Descriptor)

		// deduplication Goroutine
		g.Go(func() error {
			defer close(alreadyConverted[idx])
			// try to find chainID -> converted digest conversion if available
			desc, err := b.engine.CheckForConvertedLayer(rctx, idx)
			if err != nil {
				// in the event of failure fallback to regular process
				return nil
			}
			select {
			case <-rctx.Done():
			case alreadyConverted[idx] <- &desc:
			}

			return nil
		})

		// download goroutine
		g.Go(func() error {
			var cachedLayer *v1.Descriptor
			select {
			case <-rctx.Done():
			case cachedLayer = <-alreadyConverted[idx]:
			}

			defer close(downloaded[idx])
			if cachedLayer != nil {
				// download the converted layer
				err := b.engine.DownloadConvertedLayer(rctx, idx, *cachedLayer)
				if err == nil {
					logrus.Infof("downloaded cached layer %d", idx)
					sendToChannel(rctx, downloaded[idx], nil)
					return nil
				}
				logrus.Infof("failed to download cached layer %d falling back to conversion : %s", idx, err)
			}

			if err := b.engine.DownloadLayer(rctx, idx); err != nil {
				return err
			}
			logrus.Infof("downloaded layer %d", idx)
			sendToChannel(rctx, downloaded[idx], nil)
			return nil
		})

		// convert goroutine
		g.Go(func() error {
			defer close(converted[idx])
			if waitForChannel(rctx, downloaded[idx]); rctx.Err() != nil {
				return rctx.Err()
			}
			if idx > 0 {
				if waitForChannel(rctx, converted[idx-1]); rctx.Err() != nil {
					return rctx.Err()
				}
			}
			if err := b.engine.BuildLayer(rctx, idx); err != nil {
				return fmt.Errorf("failed to convert layer %d: %w", idx, err)
			}
			logrus.Infof("layer %d converted", idx)
			// send to upload(idx) and convert(idx+1) once each
			sendToChannel(rctx, converted[idx], nil)
			if idx+1 < b.layers {
				sendToChannel(rctx, converted[idx], nil)
			}
			return nil
		})

		g.Go(func() error {
			if waitForChannel(rctx, converted[idx]); rctx.Err() != nil {
				return rctx.Err()
			}
			if err := b.engine.UploadLayer(rctx, idx); err != nil {
				return fmt.Errorf("failed to upload layer %d: %w", idx, err)
			}
			b.engine.StoreConvertedLayerDetails(rctx, idx)
			logrus.Infof("layer %d uploaded", idx)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	if err := b.engine.UploadImage(ctx); err != nil {
		return errors.Wrap(err, "failed to upload manifest or config")
	}
	logrus.Info("convert finished")
	return nil
}

// block until ctx.Done() or sent
func sendToChannel(ctx context.Context, ch chan<- error, value error) {
	select {
	case <-ctx.Done():
	case ch <- value:
	}
}

// block until ctx.Done() or received
func waitForChannel(ctx context.Context, ch <-chan error) {
	select {
	case <-ctx.Done():
	case <-ch:
	}
}
