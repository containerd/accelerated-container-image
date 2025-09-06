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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/reference"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type BuilderOptions struct {
	Ref       string
	TargetRef string
	Auth      string
	PlainHTTP bool
	WorkDir   string
	OCI       bool
	FsType    string
	Mkfs      bool
	Vsize     int
	DB        database.ConversionDatabase
	Engine    BuilderEngineType
	CertOption
	Reserve      bool
	NoUpload     bool
	DumpManifest bool

	// ConcurrencyLimit limits the number of manifests that can be built at once
	// 0 means no limit
	ConcurrencyLimit int

	// disable sparse file when converting overlaybd
	DisableSparse bool

	// Push manifests with subject
	Referrer bool
}

type graphBuilder struct {
	// required
	Resolver remotes.Resolver

	// options
	BuilderOptions

	// private
	fetcher   remotes.Fetcher
	pusher    remotes.Pusher
	tagPusher remotes.Pusher
	group     *errgroup.Group
	sem       chan struct{}
	id        atomic.Int32
}

func (b *graphBuilder) Build(ctx context.Context) error {
	fetcher, err := b.Resolver.Fetcher(ctx, b.Ref)
	if err != nil {
		return fmt.Errorf("failed to obtain new fetcher: %w", err)
	}
	pusher, err := b.Resolver.Pusher(ctx, b.TargetRef+"@") // append '@' to avoid tag
	if err != nil {
		return fmt.Errorf("failed to obtain new pusher: %w", err)
	}
	tagPusher, err := b.Resolver.Pusher(ctx, b.TargetRef) // append '@' to avoid tag
	if err != nil {
		return fmt.Errorf("failed to obtain new tag pusher: %w", err)
	}
	b.fetcher = fetcher
	b.pusher = pusher
	b.tagPusher = tagPusher
	_, src, err := b.Resolver.Resolve(ctx, b.Ref)
	if err != nil {
		return fmt.Errorf("failed to resolve: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	b.group = g
	if b.ConcurrencyLimit > 0 {
		b.sem = make(chan struct{}, b.ConcurrencyLimit)
	}
	g.Go(func() error {
		target, err := b.process(gctx, src, true)
		if err != nil {
			return fmt.Errorf("failed to build %q: %w", src.Digest, err)
		}
		log.G(gctx).Infof("converted to %q, digest: %q", b.TargetRef, target.Digest)
		return nil
	})
	return g.Wait()
}

func (b *graphBuilder) process(ctx context.Context, src v1.Descriptor, tag bool) (v1.Descriptor, error) {
	switch src.MediaType {
	case v1.MediaTypeImageManifest, images.MediaTypeDockerSchema2Manifest:
		return b.buildOne(ctx, src, tag)
	case v1.MediaTypeImageIndex, images.MediaTypeDockerSchema2ManifestList:
		var index v1.Index
		rc, err := b.fetcher.Fetch(ctx, src)
		if err != nil {
			return v1.Descriptor{}, fmt.Errorf("failed to fetch index: %w", err)
		}
		defer rc.Close()
		indexBytes, err := io.ReadAll(rc)
		if err != nil {
			return v1.Descriptor{}, fmt.Errorf("failed to read index: %w", err)
		}
		if err := json.Unmarshal(indexBytes, &index); err != nil {
			return v1.Descriptor{}, fmt.Errorf("failed to unmarshal index: %w", err)
		}
		var wg sync.WaitGroup
		for _i, _m := range index.Manifests {
			i := _i
			m := _m
			wg.Add(1)
			b.group.Go(func() error {
				defer wg.Done()
				target, err := b.process(ctx, m, false)
				if err != nil {
					return fmt.Errorf("failed to build %q: %w", m.Digest, err)
				}
				index.Manifests[i] = target
				return nil
			})
		}
		wg.Wait()
		if ctx.Err() != nil {
			return v1.Descriptor{}, ctx.Err()
		}

		// upload index
		if b.Referrer {
			index.ArtifactType = b.Engine.ArtifactType()
			index.Subject = &v1.Descriptor{
				MediaType: src.MediaType,
				Digest:    src.Digest,
				Size:      src.Size,
			}
		}
		if b.OCI {
			index.MediaType = v1.MediaTypeImageIndex
		}
		indexBytes, err = json.Marshal(index)
		if err != nil {
			return v1.Descriptor{}, fmt.Errorf("failed to marshal index: %w", err)
		}
		if b.DumpManifest {
			if err := os.WriteFile(filepath.Join(b.WorkDir, "index.json"), indexBytes, 0644); err != nil {
				return v1.Descriptor{}, fmt.Errorf("failed to dump index: %w", err)
			}
		}
		expected := v1.Descriptor{
			MediaType: index.MediaType,
			Digest:    digest.FromBytes(indexBytes),
			Size:      int64(len(indexBytes)),
		}
		var pusher remotes.Pusher
		if tag {
			pusher = b.tagPusher
		} else {
			pusher = b.pusher
		}
		if err := uploadBytes(ctx, pusher, expected, indexBytes); err != nil {
			return v1.Descriptor{}, fmt.Errorf("failed to upload index: %w", err)
		}
		log.G(ctx).Infof("index uploaded, %s", expected.Digest)
		return expected, nil
	default:
		return v1.Descriptor{}, fmt.Errorf("unsupported media type %q", src.MediaType)
	}
}

func (b *graphBuilder) buildOne(ctx context.Context, src v1.Descriptor, tag bool) (v1.Descriptor, error) {
	if b.sem != nil {
		select {
		case <-ctx.Done():
			return v1.Descriptor{}, ctx.Err()
		case b.sem <- struct{}{}:
		}
	}
	defer func() {
		if b.sem != nil {
			select {
			case <-ctx.Done():
			case <-b.sem:
			}
		}
	}()
	id := b.id.Add(1)

	var platform string
	if src.Platform == nil {
		platform = ""
	} else {
		platform = platforms.Format(*src.Platform)
		ctx = log.WithLogger(ctx, log.G(ctx).WithField("platform", platform))
	}
	workdir := filepath.Join(b.WorkDir, fmt.Sprintf("%d-%s-%s", id, strings.ReplaceAll(platform, "/", "_"), src.Digest.Encoded()))
	log.G(ctx).Infof("building %s ...", workdir)

	// init build engine
	manifest, config, err := fetchManifestAndConfig(ctx, b.fetcher, src)
	if err != nil {
		return v1.Descriptor{}, fmt.Errorf("failed to fetch manifest and config: %w", err)
	}
	var pusher remotes.Pusher
	if tag {
		pusher = b.tagPusher
	} else {
		pusher = b.pusher
	}
	engineBase := &builderEngineBase{
		resolver:  b.Resolver,
		fetcher:   b.fetcher,
		pusher:    pusher,
		manifest:  *manifest,
		config:    *config,
		inputDesc: src,
		referrer:  b.Referrer,
	}
	engineBase.workDir = workdir
	engineBase.oci = b.OCI
	engineBase.fstype = b.FsType
	engineBase.mkfs = b.Mkfs
	engineBase.vsize = b.Vsize
	engineBase.db = b.DB
	refspec, err := reference.Parse(b.Ref)
	if err != nil {
		return v1.Descriptor{}, err
	}
	engineBase.host = refspec.Hostname()
	engineBase.repository = strings.TrimPrefix(refspec.Locator, engineBase.host+"/")
	engineBase.reserve = b.Reserve
	engineBase.noUpload = b.NoUpload
	engineBase.dumpManifest = b.DumpManifest

	var engine builderEngine
	switch b.Engine {
	case Overlaybd:
		engine = NewOverlayBDBuilderEngine(engineBase)
		engine.(*overlaybdBuilderEngine).disableSparse = b.DisableSparse
	case TurboOCI:
		engine = NewTurboOCIBuilderEngine(engineBase)
	}

	// build
	builder := &overlaybdBuilder{
		layers: len(engineBase.manifest.Layers),
		engine: engine,
	}
	desc, err := builder.Build(ctx)
	if err != nil {
		return v1.Descriptor{}, fmt.Errorf("failed to build %s: %w", workdir, err)
	}

	// preserve the other fields from src descriptor
	src.Digest = desc.Digest
	src.Size = desc.Size
	src.MediaType = desc.MediaType
	return src, nil
}

func Build(ctx context.Context, opt BuilderOptions) error {
	tlsConfig, err := loadTLSConfig(opt.CertOption)
	if err != nil {
		return fmt.Errorf("failed to load certifications: %w", err)
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:       30 * time.Second,
			KeepAlive:     30 * time.Second,
			FallbackDelay: 300 * time.Millisecond,
		}).DialContext,
		MaxConnsPerHost:       32, // max http concurrency
		MaxIdleConns:          32,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		TLSClientConfig:       tlsConfig,
		ExpectContinueTimeout: 5 * time.Second,
	}
	client := &http.Client{Transport: transport}
	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(docker.NewDockerAuthorizer(
				docker.WithAuthClient(client),
				docker.WithAuthHeader(make(http.Header)),
				docker.WithAuthCreds(func(s string) (string, string, error) {
					if i := strings.IndexByte(opt.Auth, ':'); i > 0 {
						return opt.Auth[0:i], opt.Auth[i+1:], nil
					}
					return "", "", nil
				}),
			)),
			docker.WithClient(client),
			docker.WithPlainHTTP(func(s string) (bool, error) {
				if opt.PlainHTTP {
					return docker.MatchAllHosts(s)
				} else {
					return false, nil
				}
			}),
		),
	})

	return (&graphBuilder{
		Resolver:       resolver,
		BuilderOptions: opt,
	}).Build(ctx)
}

type overlaybdBuilder struct {
	layers int
	engine builderEngine
}

// Build return a descriptor of the converted target, as the caller may need it
// to tag or compose an index
func (b *overlaybdBuilder) Build(ctx context.Context) (v1.Descriptor, error) {
	defer b.engine.Cleanup()
	alreadyConverted := make([]chan *v1.Descriptor, b.layers)
	downloaded := make([]chan error, b.layers)
	converted := make([]chan error, b.layers)

	// check if manifest conversion result is already present in registry, if so, we can avoid conversion.
	// when errors are encountered fallback to regular conversion
	if convertedDesc, err := b.engine.CheckForConvertedManifest(ctx); err == nil && convertedDesc.Digest != "" {
		logrus.Infof("Image found already converted in registry with digest %s", convertedDesc.Digest)
		// Even if the image has been found we still need to make sure the requested tag is set
		// fetch the manifest then push again with the requested tag
		if err := b.engine.TagPreviouslyConvertedManifest(ctx, convertedDesc); err != nil {
			logrus.Warnf("failed to tag previously converted manifest: %s. Falling back to regular conversion", err)
		} else {
			return convertedDesc, nil
		}
	}

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
		return v1.Descriptor{}, err
	}

	targetDesc, err := b.engine.UploadImage(ctx)
	if err != nil {
		return v1.Descriptor{}, fmt.Errorf("failed to upload manifest or config: %w", err)
	}
	b.engine.StoreConvertedManifestDetails(ctx)
	logrus.Info("convert finished")
	return targetDesc, nil
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

// -------------------- certification --------------------
type CertOption struct {
	CertDirs    []string
	RootCAs     []string
	ClientCerts []string
	Insecure    bool
}

func loadTLSConfig(opt CertOption) (*tls.Config, error) {
	type clientCertPair struct {
		certFile string
		keyFile  string
	}
	var clientCerts []clientCertPair
	// client certs from option `--client-cert`
	for _, cert := range opt.ClientCerts {
		s := strings.Split(cert, ":")
		if len(s) != 2 {
			return nil, fmt.Errorf("client cert %s: invalid format", cert)
		}
		clientCerts = append(clientCerts, clientCertPair{
			certFile: s[0],
			keyFile:  s[1],
		})
	}
	// root CAs / client certs from option `--cert-dir`
	for _, d := range opt.CertDirs {
		fs, err := os.ReadDir(d)
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("failed to read cert directory %q: %w", d, err)
		}
		for _, f := range fs {
			if strings.HasSuffix(f.Name(), ".crt") {
				opt.RootCAs = append(opt.RootCAs, filepath.Join(d, f.Name()))
			}
			if strings.HasSuffix(f.Name(), ".cert") {
				clientCerts = append(clientCerts, clientCertPair{
					certFile: filepath.Join(d, f.Name()),
					keyFile:  filepath.Join(d, strings.TrimSuffix(f.Name(), ".cert")+".key"),
				})
			}
		}
	}
	tlsConfig := &tls.Config{}
	// root CAs from ENV ${SSL_CERT_FILE} and ${SSL_CERT_DIR}
	systemPool, err := x509.SystemCertPool()
	if err != nil {
		if runtime.GOOS == "windows" {
			systemPool = x509.NewCertPool()
		} else {
			return nil, fmt.Errorf("failed to get system cert pool: %w", err)
		}
	}
	tlsConfig.RootCAs = systemPool
	// root CAs from option `--root-ca`
	for _, file := range opt.RootCAs {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read root CA file %q: %w", file, err)
		}
		tlsConfig.RootCAs.AppendCertsFromPEM(b)
	}
	// load client certs
	for _, c := range clientCerts {
		cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client cert pair {%q, %q}: %w", c.certFile, c.keyFile, err)
		}
		tlsConfig.Certificates = append(tlsConfig.Certificates, cert)
	}
	tlsConfig.InsecureSkipVerify = opt.Insecure
	return tlsConfig, nil
}
