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

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p"
	log "github.com/sirupsen/logrus"
)

type arrayFlags []string

func (i *arrayFlags) String() string {
	return strings.Join(*i, " ")
}

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}

var (
	root           arrayFlags
	port           int
	prefetch       int
	cacheSize      int64
	maxEntry       int64
	loglevel       string
	media          string
	nodeIP         string
	detectAddr     string
	certPath       string
	certKeyPath    string
	isGenerateCert bool
)

func getOutboundIP() net.IP {
	conn, err := net.Dial("udp", detectAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer func(conn net.Conn) {
		_ = conn.Close()
	}(conn)
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP
}

func getRealPath(rawPath string) string {
	realPath, _ := filepath.Abs(rawPath)
	return realPath
}

func init() {
	flag.Var(&root, "r", "Root list")
	flag.StringVar(&nodeIP, "h", "", "Current node IP Address")
	flag.StringVar(&detectAddr, "d", "8.8.8.8:80", "Address for detecting current node address")
	flag.IntVar(&port, "p", 19145, "Listening port")
	flag.Int64Var(&cacheSize, "m", 8*1024*1024*1024, "Cache size")
	flag.Int64Var(&maxEntry, "e", 1*1024*1024*1024, "Max cache entry")
	flag.IntVar(&prefetch, "pre", 64, "Prefetch workers")
	flag.StringVar(&loglevel, "l", "info", "Log level, debug | info | warn | error | panic")
	flag.StringVar(&media, "c", "/tmp/cache", "Cache media path")
	flag.StringVar(&certPath, "cp", "p2pcert.pem", "Certificate path")
	flag.StringVar(&certKeyPath, "ckp", "p2pcert.key", "Certificate key path")
	flag.BoolVar(&isGenerateCert, "generate-cert", false, "Is generate certificate flag")
}

func main() {
	flag.Parse()
	certPath = getRealPath(certPath)
	certKeyPath = getRealPath(certKeyPath)
	level, err := log.ParseLevel(loglevel)
	if err != nil {
		level = log.InfoLevel
		log.Warnf("Log level argument %s not recognized", loglevel)
	}
	log.SetLevel(level)
	rand.Seed(time.Now().Unix())
	if len(root) == 0 {
		log.Info("P2P Root")
	} else {
		log.Info("P2P Agent")
	}
	cache := p2p.NewCachePool(&p2p.CacheConfig{
		MaxEntry:   maxEntry,
		CacheSize:  cacheSize,
		CacheMedia: media,
	})
	hp := p2p.NewHostPicker(root, cache)
	fs := p2p.NewP2PFS(&p2p.FSConfig{
		CachePool:       cache,
		HostPicker:      hp,
		APIKey:          "dadip2p",
		PrefetchWorkers: prefetch,
	})
	if nodeIP == "" {
		nodeIP = getOutboundIP().String()
	}
	serverHandler := p2p.NewP2PServer(&p2p.ServerConfig{
		MyAddr: fmt.Sprintf("http://%s:%d", nodeIP, port),
		Fs:     fs,
		APIKey: "dadip2p",
	})
	cert := p2p.GetRootCA(certPath, certKeyPath, isGenerateCert)
	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", port),
		Handler:   serverHandler,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{*cert}},
	}
	panic(server.ListenAndServe())
}
