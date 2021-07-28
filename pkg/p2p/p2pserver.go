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

package p2p

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// ServerConfig configuration for server
type ServerConfig struct {
	// Host address, for P2P route
	MyAddr string
	// APIKey as prefix for P2P Access
	APIKey string
	// Fs cached remote file system for p2p
	Fs *FS
	// IsRoot set if server is root, will not fetch by p2p if it is a root
	IsRoot bool
}

// Server object for p2p and proxy
type Server struct {
	config *ServerConfig
	cm     ChildrenManager
}

func (s Server) proxyHandler(w http.ResponseWriter, req *http.Request) {
	u, err := url.Parse(fmt.Sprintf("%s://%s", req.URL.Scheme, req.URL.Host))
	if err != nil {
		log.Error("Parse url failed")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.Warnf("Proxy to %s", u)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ServeHTTP(w, req)
}

type closableReadWriter interface {
	net.Conn
	CloseWrite() error
	CloseRead() error
}

func copyAndClose(dst closableReadWriter, src closableReadWriter) {
	io.Copy(dst, src)
	src.CloseRead()
	dst.CloseWrite()
}

func (s Server) connectHandler(w http.ResponseWriter, req *http.Request) {
	host := req.URL.Host
	if match, err := regexp.MatchString(`.*:\d+`, host); !match && err == nil {
		host += ":80"
	}
	targetSiteCon, err := net.Dial("tcp", host)
	if err != nil {
		log.Error("Failed to connect ", host)
		http.Error(w, "Connect failed", http.StatusNotFound)
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
	proxyClient.Write([]byte("HTTP/1.0 200 OK\r\n\r\n"))
	go copyAndClose(targetSiteCon.(*net.TCPConn), proxyClient.(*net.TCPConn))
	go copyAndClose(proxyClient.(*net.TCPConn), targetSiteCon.(*net.TCPConn))
}

func (s Server) redirectHandler(w http.ResponseWriter, req *http.Request, redirectHost string, url string) {
	http.Redirect(w, req,
		fmt.Sprintf("%s/%s/%s", redirectHost, s.config.APIKey, url),
		http.StatusTemporaryRedirect)
}

func (s Server) p2pHandler(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(r)
			ff := r.(FetchFailure)
			if ff.err != nil {
				log.Error("Request failed ", ff.err.Error())
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				log.Errorf("Request failed %d", ff.resp.StatusCode)
				for k, vs := range ff.resp.Header {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(ff.resp.StatusCode)
				io.Copy(w, ff.resp.Body)
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
			s.redirectHandler(w, req, redirect, reqURL)
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

// ServeHTTP as HTTP handler
func (s Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	match, _ := regexp.MatchString(".*/v2/blobs/sha256/.*/data", req.URL.Path)
	log.Infof("Accept request %s %s match: %t", req.Method, req.URL.Path, match)
	if match && req.Method == "GET" {
		if !strings.HasPrefix(req.URL.Path, fmt.Sprintf("/%s/", s.config.APIKey)) {
			log.Info("Redirect")
			s.redirectHandler(w, req, s.config.MyAddr, req.URL.String())
		} else {
			log.Info("P2P")
			s.p2pHandler(w, req)
		}
	} else if req.Method == "CONNECT" {
		log.Info("Connect")
		s.connectHandler(w, req)
	} else {
		log.Info("Proxy")
		s.proxyHandler(w, req)
	}
}

// NewP2PServer creator for p2p proxy server
func NewP2PServer(config *ServerConfig) *Server {
	if config.APIKey == "" {
		config.APIKey = "dadip2p"
	}
	ret := Server{config, NewLimitedChildrenManager(2, time.Minute)}
	return &ret
}
