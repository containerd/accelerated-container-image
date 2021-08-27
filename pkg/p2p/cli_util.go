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
	"crypto/tls"
	"fmt"
	"net/http"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func CheckConfig(config *DeployConfig) {
	// run mode
	if config.RunMode != "root" && config.RunMode != "agent" {
		log.Fatalf("Unexpected run mode %s!", config.RunMode)
	}
	// prefetch
	if !config.PrefetchConfig.PrefetchEnable {
		config.PrefetchConfig.PrefetchThread = 0
	}
	// file cache
	if !config.CacheConfig.FileCacheEnable {
		config.CacheConfig.FileCacheSize = 0
	}
	// memory size
	if !config.CacheConfig.MemCacheEnable {
		config.CacheConfig.MemCacheSize = 0
	}
	// certificate
	if config.CertConfig.CertEnable {
		config.CertConfig.CertPath = GetRealPath(config.CertConfig.CertPath)
		config.CertConfig.KeyPath = GetRealPath(config.CertConfig.KeyPath)
	}
	// nodeIP
	if config.NodeIP == "" {
		config.NodeIP = GetOutboundIP(config.DetectAddr).String()
	}
	// log
	level, err := log.ParseLevel(config.LogLevel)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)
	// root list
	for idx := range config.RootList {
		if !config.ServeBySSL {
			config.RootList[idx] = fmt.Sprintf("http://%s", config.RootList[idx])
		} else {
			config.RootList[idx] = fmt.Sprintf("https://%s", config.RootList[idx])
		}
	}
}

func InitConfig(cfgFile string) *DeployConfig {
	var config = &DeployConfig{}
	viper.SetConfigFile(cfgFile)
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Read file err! %s", err)
	}
	if err := viper.Unmarshal(config); err != nil {
		log.Fatalf("Unmarshal file err! %s", err)
	}
	log.Println("Using config file:", viper.ConfigFileUsed())
	return config
}

func Execute(config *DeployConfig, isRun bool) *http.Server {
	switch config.RunMode {
	case "root":
		log.Info("Run on P2P Root")
	case "agent":
		log.Info("Run on P2P Agent")
	}
	cache := NewCachePool(&CacheConfig{
		MaxEntry:   config.CacheConfig.MemCacheSize,
		CacheSize:  config.CacheConfig.FileCacheSize,
		CacheMedia: config.CacheConfig.FileCachePath,
	})
	hp := NewHostPicker(config.RootList, cache)
	fs := NewP2PFS(&FSConfig{
		CachePool:       cache,
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
	serverHandler := NewP2PServer(&ServerConfig{
		MyAddr:     myAddr,
		Fs:         fs,
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
		panic(server.ListenAndServe())
	}
	return server
}
