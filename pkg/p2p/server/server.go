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
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/cache"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/configure"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/fs"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/hostselector"

	log "github.com/sirupsen/logrus"
)

// Execute used to get server by config, isRun set will start server immediately instead of return a server.
func Execute(config *configure.DeployConfig, isRun bool) *http.Server {
	switch config.RunMode {
	case "root":
		log.Info("Run on P2P Root")
	case "agent":
		log.Info("Run on P2P Agent")
	}
	cachePool := cache.NewCachePool(&cache.Config{
		MaxEntry:   config.CacheConfig.MemCacheSize,
		CacheSize:  config.CacheConfig.FileCacheSize,
		CacheMedia: config.CacheConfig.FileCachePath,
	})
	hp := hostselector.NewHostPicker(config.RootList, cachePool)
	p2pFs := fs.NewP2PFS(&fs.Config{
		CachePool:       cachePool,
		HostPicker:      hp,
		APIKey:          "dadip2p",
		PrefetchWorkers: config.PrefetchConfig.PrefetchThread,
	})
	var myAddr string
	if !config.ServeBySSL {
		myAddr = fmt.Sprintf("http://%s:%d", config.NodeIP, config.Port)
	} else {
		myAddr = fmt.Sprintf("https://%s:%d", config.NodeIP, config.Port)
	}
	serverHandler := NewP2PServer(&Config{
		MyAddr:     myAddr,
		Fs:         p2pFs,
		APIKey:     "dadip2p",
		ProxyHTTPS: config.CertConfig.CertEnable,
	})
	var cert []tls.Certificate
	if config.CertConfig.CertEnable {
		cert = []tls.Certificate{*GetRootCA(config.CertConfig.CertPath, config.CertConfig.KeyPath, config.CertConfig.GenerateCert)}
	}
	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", config.Port),
		Handler:   serverHandler,
		TLSConfig: &tls.Config{Certificates: cert},
	}
	if isRun {
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("Server stop! %s", err)
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
	// IsRoot set if server is root, will not fetch by p2p if it is a root
	IsRoot bool
	// ProxyHTTPS set if proxy by https
	ProxyHTTPS bool
}

// Server object for p2p and proxy
type Server struct {
	config    *Config
	cm        hostselector.ChildrenManager
	transport *http.Transport
}

// ServeHTTP as HTTP handler
func (s Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		if s.config.ProxyHTTPS {
			s.httpsHandler(w, req)
		} else {
			s.connectHandler(w, req)
		}
	} else {
		s.httpHandler(w, req)
	}
}

const matchPattern = ".*/v2/blobs/sha256/.*/data"

func (s Server) httpHandler(w http.ResponseWriter, req *http.Request) {
	match, _ := regexp.MatchString(matchPattern, req.URL.Path)
	log.Infof("Accept request %s %s://%s%s match: %t", req.Method, req.URL.Scheme, req.URL.Host, req.URL.Path, match)
	if match && req.Method == http.MethodGet {
		if !strings.HasPrefix(req.URL.Path, fmt.Sprintf("/%s/", s.config.APIKey)) {
			log.Info("Redirect")
			s.redirectHTTPHandler(w, req, s.config.MyAddr, req.URL.String())
		} else {
			log.Info("P2P")
			s.p2pHandler(w, req)
		}
	} else {
		log.Info("Proxy")
		err := s.proxyHTTPHandler(w, req)
		if err != nil {
			log.Errorf("Proxy request fail! %s", err)
			http.Error(w, "Proxy request fail!", http.StatusInternalServerError)
			return
		}
	}
}

var InternalServerErrorBytes = []byte(fmt.Sprintf("HTTP/1.1 %d %s\r\n\r\n", http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)))

func (s Server) httpsHandler(w http.ResponseWriter, req *http.Request) {
	//https request need decode
	conn, req, err := s.decodeHTTPSRequest(w, req)
	if err != nil {
		log.Errorf("Decode https request fail! %s", err)
		return
	}
	defer func(conn *tls.Conn) {
		_ = conn.Close()
	}(conn)
	match, _ := regexp.MatchString(matchPattern, req.URL.Path)
	log.Infof("Accept request %s %s://%s%s match: %t", req.Method, req.URL.Scheme, req.URL.Host, req.URL.Path, match)
	if match && req.Method == http.MethodGet {
		log.Info("Redirect")
		err = s.redirectHTTPSHandler(conn, s.config.MyAddr, req.URL.String())
		if err != nil {
			log.Errorf("Redirect request fail! %s", err)
			_, _ = conn.Write(InternalServerErrorBytes)
			return
		}
	} else {
		log.Info("Proxy")
		err = s.proxyHTTPSHandler(conn, req)
		if err != nil {
			log.Errorf("Proxy request fail! %s", err)
			_, _ = conn.Write(InternalServerErrorBytes)
			return
		}
	}
}

func (s Server) connectHandler(w http.ResponseWriter, req *http.Request) {
	host := req.URL.Host
	if match, err := regexp.MatchString(`.*:\d+`, host); !match && err == nil {
		host += ":80"
	}
	targetSiteCon, err := net.Dial("tcp", host)
	if err != nil {
		log.Error("Failed to connect ", host)
		http.Error(w, "Connect fail!", http.StatusNotFound)
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		log.Error("Failed get client connection")
		http.Error(w, "Webserver failed to hijacking", http.StatusInternalServerError)
		return
	}
	proxyClient, _, err := h.Hijack()
	if err != nil {
		http.Error(w, "Webserver failed to hijacking", http.StatusInternalServerError)
		return
	}
	log.Debugf("Accepting CONNECT to %s", host)
	if _, err := proxyClient.Write([]byte("HTTP/1.0 200 OK\r\n\r\n")); err != nil {
		log.Errorf("")
	}
	go copyAndClose(targetSiteCon.(*net.TCPConn), proxyClient.(*net.TCPConn))
	go copyAndClose(proxyClient.(*net.TCPConn), targetSiteCon.(*net.TCPConn))
}

type closableReadWriter interface {
	net.Conn
	CloseWrite() error
	CloseRead() error
}

func copyAndClose(dst closableReadWriter, src closableReadWriter) {
	_, _ = io.Copy(dst, src)
	_ = src.CloseRead()
	_ = dst.CloseWrite()
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
	log.Info(req.Header)
	defer log.Info(w.Header())
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
	log.Debug("Cache Request ", fn, reqURL, newReq.Header.Get("X-P2P-Agent"), newReq.Header.Get("Range"))
	file, err := s.config.Fs.Open(fn, newReq)
	if err != nil {
		panic(err)
	} else {
		log.Debug("Serving ", file)
		w.Header().Set("X-P2P-Source", s.config.MyAddr)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Del("Content-Length")
		http.ServeContent(w, req, "file", time.Now(), file)
		log.Debug("Serve request done ", fn, w.Header())
	}
}

func (s Server) decodeHTTPSRequest(w http.ResponseWriter, req *http.Request) (*tls.Conn, *http.Request, error) {
	host := req.URL.Host
	h, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("failed get client connection")
	}
	proxyClient, _, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	_, err = proxyClient.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	if err != nil {
		return nil, nil, err
	}
	noPortHost, _, _ := net.SplitHostPort(host)
	tlsConfig := generateTLSConfig(noPortHost)
	tlsClient := tls.Server(proxyClient, tlsConfig)
	err = tlsClient.Handshake()
	if err != nil {
		return nil, nil, err
	}
	buf := bufio.NewReader(tlsClient)
	tlsReq, err := http.ReadRequest(buf)
	if err != nil {
		return nil, nil, err
	}
	tlsReq.RemoteAddr = req.RemoteAddr
	tlsReq.URL.Scheme = "https"
	tlsReq.URL.Host = tlsReq.Host
	return tlsClient, tlsReq, nil
}

func (s Server) redirectHTTPHandler(w http.ResponseWriter, req *http.Request, redirectHost string, url string) {
	redirectURL := fmt.Sprintf("%s/%s/%s", redirectHost, s.config.APIKey, url)
	http.Redirect(w, req, redirectURL, http.StatusTemporaryRedirect)
}

func (s Server) redirectHTTPSHandler(w *tls.Conn, redirectHost string, url string) error {
	redirectURL := fmt.Sprintf("%s/%s/%s", redirectHost, s.config.APIKey, url)
	log.Infof("Redirect to %s", redirectURL)
	redirectResponseBytes := []byte(
		fmt.Sprintf("HTTP/1.1 %d %s\r\nLocation: %s\r\n\r\n",
			http.StatusTemporaryRedirect,
			http.StatusText(http.StatusTemporaryRedirect),
			redirectURL,
		))
	if _, err := w.Write(redirectResponseBytes); err != nil {
		return err
	}
	return nil
}

func (s Server) proxyHTTPHandler(w http.ResponseWriter, req *http.Request) error {
	u, err := url.Parse(fmt.Sprintf("%s://%s", req.URL.Scheme, req.URL.Host))
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = s.transport
	proxy.ServeHTTP(w, req)
	return nil
}

func (s Server) proxyHTTPSHandler(w *tls.Conn, req *http.Request) error {
	resp, err := s.transport.RoundTrip(req)
	defer func() {
		_ = resp.Body.Close()
	}()
	if err != nil {
		return err
	}
	err = resp.Write(w)
	if err != nil {
		return err
	}
	return nil
}

// NewP2PServer creator for p2p proxy server
func NewP2PServer(config *Config) *Server {
	return &Server{
		config: config,
		cm:     hostselector.NewLimitedChildrenManager(2, 1*time.Minute),
		transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			MaxIdleConns:    100,
		},
	}
}
