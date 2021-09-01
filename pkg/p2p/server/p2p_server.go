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

package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/cache"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/certificate"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/configure"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/fs"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/hostselector"

	log "github.com/sirupsen/logrus"
)

func StartP2PServer(config *configure.DeployConfig, isRun bool) *http.Server {
	switch config.P2PConfig.RunMode {
	case "root":
		log.Info("Run on P2P Root")
	case "agent":
		log.Info("Run on P2P Agent")
	}
	cachePool := cache.NewCachePool(&cache.Config{
		MaxEntry:   config.P2PConfig.CacheConfig.MemCacheSize,
		CacheSize:  config.P2PConfig.CacheConfig.FileCacheSize,
		CacheMedia: config.P2PConfig.CacheConfig.FileCachePath,
	})
	hp := hostselector.NewHostPicker(config.P2PConfig.RootList, cachePool)
	p2pFs := fs.NewP2PFS(&fs.Config{
		CachePool:       cachePool,
		HostPicker:      hp,
		APIKey:          config.APIKey,
		PrefetchWorkers: config.P2PConfig.PrefetchConfig.PrefetchThread,
	})
	serverHandler := newP2PServer(&Config{
		MyAddr: config.P2PConfig.MyAddr,
		Fs:     p2pFs,
		APIKey: config.APIKey,
	})
	addr := fmt.Sprintf(":%d", config.P2PConfig.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: serverHandler,
	}
	if isRun {
		if config.P2PConfig.ServeBySSL {
			certificate.GetRootCA(config.ProxyConfig.CertConfig.CertPath, config.ProxyConfig.CertConfig.KeyPath, config.ProxyConfig.CertConfig.GenerateCert)
			cert, key := certificate.GenerateCertificate(config.P2PConfig.NodeIP)
			if err := ioutil.WriteFile("/tmp/httpcert.pem", cert, 0644); err != nil {
				log.Fatalf("Write file httpcert.pem failed! %s", err)
			}
			if err := ioutil.WriteFile("/tmp/httpcert.key", key, 0644); err != nil {
				log.Fatalf("Write file httpcert.key failed! %s", err)
			}
			if err := server.ListenAndServeTLS("/tmp/httpcert.pem", "/tmp/httpcert.key"); err != nil {
				log.Errorf("P2P server %s stop! %s", addr, err)
			}
		} else {
			if err := server.ListenAndServe(); err != nil {
				log.Errorf("P2P server %s stop! %s", addr, err)
			}
		}
	}
	return server
}

// Config configuration for server
type Config struct {
	// Host address, for P2P route
	MyAddr string
	// APIKey as prefix for P2P Access
	APIKey string
	// Fs cached remote file system for p2p
	Fs *fs.P2PFS
}

// Server object for p2p and proxy
type Server struct {
	config    *Config
	cm        hostselector.ChildrenManager
	transport *http.Transport
}

// ServeHTTP as HTTP handler
func (s Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.p2pHandler(w, req)
}

func (s Server) p2pHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(r)
			ff := r.(fs.FetchFailure)
			if ff.Err != nil {
				log.Error("Request failed ", ff.Err)
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				log.Errorf("Request failed %d", ff.Resp.StatusCode)
				for k, vs := range ff.Resp.Header {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(ff.Resp.StatusCode)
				_, err := io.Copy(w, ff.Resp.Body)
				if err != nil {
					log.Errorf("IO copy fail! %s", err)
				}
			}
		}
	}()
	log.Debug(req.Header)
	defer log.Debug(w.Header())
	agents := strings.Split(req.Header.Get("X-P2P-Agent"), " ")
	agent := agents[len(agents)-1]
	fn := req.URL.Path[len(s.config.APIKey)+2:]
	reqURL := fmt.Sprintf("%s?%s", fn, req.URL.RawQuery)
	if len(agent) > 0 {
		if accepted, redirect := s.cm.TryAccept(fn, agent); !accepted {
			log.Infof("Request for %s from %s redirected to %s", fn, agent, redirect)
			s.redirectHTTPHandler(w, req, redirect, reqURL)
			return
		}
		log.Infof("Accept child %s for %s", agent, fn)
	}
	newReq, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		panic(err)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			newReq.Header.Add(k, v)
		}
	}
	newReq.Header.Add("X-P2P-Agent", s.config.MyAddr)
	log.Debugf("Cache Request %s %s %s", fn, newReq.Header.Get("X-P2P-Agent"), newReq.Header.Get("Range"))
	file, err := s.config.Fs.Open(fn, newReq)
	if err != nil {
		panic(err)
	} else {
		log.Debugf("Serving %v", file)
		w.Header().Set("X-P2P-Source", s.config.MyAddr)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Del("Content-Length")
		http.ServeContent(w, req, "file", time.Now(), file)
		log.Debugf("Serve request done %s %v", fn, w.Header())
	}
}

func (s Server) redirectHTTPHandler(w http.ResponseWriter, req *http.Request, redirectHost string, url string) {
	redirectURL := fmt.Sprintf("%s/%s/%s", redirectHost, s.config.APIKey, url)
	http.Redirect(w, req, redirectURL, http.StatusTemporaryRedirect)
}

// newP2PServer creator for p2p proxy server
func newP2PServer(config *Config) *Server {
	return &Server{
		config: config,
		cm:     hostselector.NewLimitedChildrenManager(2, 1*time.Minute),
		transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			MaxConnsPerHost: 100,
		},
	}
}
