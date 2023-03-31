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
	"strings"
	"sync"

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
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
			if opt.Auth == "" {
				return "", "", nil
			}
			authSplit := strings.Split(opt.Auth, ":")
			return authSplit[0], authSplit[1], nil
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
	case BuilderEngineTypeOverlayBD:
		engine = NewOverlayBDBuilderEngine(engineBase)
	case BuilderEngineTypeFastOCI:
		engine = NewFastOCIBuilderEngine(engineBase)
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
	var uploaded sync.WaitGroup

	errCh := make(chan error)
	defer close(errCh)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// collect error and kill all builder goroutines
	var retErr error
	retErr = nil
	go func() {
		select {
		case <-ctx.Done():
		case retErr = <-errCh:
		}
		if retErr != nil {
			cancel()
		}
	}()

	for i := 0; i < b.layers; i++ {
		downloaded[i] = make(chan error)
		converted[i] = make(chan error)
		alreadyConverted[i] = make(chan *v1.Descriptor)

		// deduplication Goroutine
		go func(idx int) {
			defer close(alreadyConverted[idx])
			// try to find chainID -> converted digest conversion if available
			desc, err := b.engine.CheckForConvertedLayer(ctx, idx)
			if err != nil {
				// in the event of failure fallback to regular process
				return
			}
			alreadyConverted[idx] <- &desc
		}(i)

		// download goroutine
		go func(idx int) {
			var cachedLayer *v1.Descriptor
			select {
			case <-ctx.Done():
			case cachedLayer = <-alreadyConverted[idx]:
			}

			defer close(downloaded[idx])
			if cachedLayer != nil {
				// download the converted layer
				err := b.engine.DownloadConvertedLayer(ctx, idx, *cachedLayer)
				if err == nil {
					logrus.Infof("downloaded cached layer %d", idx)
					sendToChannel(ctx, downloaded[idx], nil)
					return
				}
				logrus.Infof("failed to download cached layer %d falling back to conversion : %s", idx, err)
			}

			if err := b.engine.DownloadLayer(ctx, idx); err != nil {
				sendToChannel(ctx, errCh, errors.Wrapf(err, "failed to download layer %d", idx))
				return
			}
			logrus.Infof("downloaded layer %d", idx)
			sendToChannel(ctx, downloaded[idx], nil)
		}(i)

		// convert goroutine
		go func(idx int) {
			defer close(converted[idx])
			if waitForChannel(ctx, downloaded[idx]); ctx.Err() != nil {
				return
			}
			if idx > 0 {
				if waitForChannel(ctx, converted[idx-1]); ctx.Err() != nil {
					return
				}
			}
			if err := b.engine.BuildLayer(ctx, idx); err != nil {
				sendToChannel(ctx, errCh, errors.Wrapf(err, "failed to convert layer %d", idx))
				return
			}
			logrus.Infof("layer %d converted", idx)
			// send to upload(idx) and convert(idx+1) once each
			sendToChannel(ctx, converted[idx], nil)
			if idx+1 < b.layers {
				sendToChannel(ctx, converted[idx], nil)
			}
		}(i)

		// upload goroutine
		uploaded.Add(1)
		go func(idx int) {
			defer uploaded.Done()
			if waitForChannel(ctx, converted[idx]); ctx.Err() != nil {
				return
			}
			if err := b.engine.UploadLayer(ctx, idx); err != nil {
				sendToChannel(ctx, errCh, errors.Wrapf(err, "failed to upload layer %d", idx))
				return
			}
			b.engine.StoreConvertedLayerDetails(ctx, idx)
			logrus.Infof("layer %d uploaded", idx)
		}(i)
	}
	uploaded.Wait()
	if retErr != nil {
		return retErr
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
