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
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	rootCmd = &cobra.Command{
		Use:   "dadip2p_server",
		Short: "dadip2p_server",
		Long:  `dadip2p_server`,
		Run: func(cmd *cobra.Command, args []string) {
			checkConfig()
			execute()
		},
	}
	cfgFile string
)

func init() {
	rand.Seed(time.Now().Unix())
	// cfg
	cobra.OnInitialize(initConfig)
	rootCmd.Flags().StringVarP(&cfgFile, "cfgFile", "", "dadip2p.yaml", "Config file (default is dadip2p.yaml)")
	// flag
	rootCmd.Flags().BoolP("generate-cert", "", false, "Is auto generate cert")
	bindFlag("CertConfig.GenerateCert", "generate-cert")
	rootCmd.Flags().StringP("log-level", "l", "info", "Log level, debug | info | warn | error | panic")
	bindFlag("LogLevel", "log-level")
	rootCmd.Flags().IntP("port", "p", 19145, "Port")
	bindFlag("Port", "port")
}

func bindFlag(configName, flagName string) {
	if err := viper.BindPFlag(configName, rootCmd.Flags().Lookup(flagName)); err != nil {
		log.Fatalf("Bind flag error! config name: %s, flag name: %s. %s", configName, flagName, err)
	}
}

func checkConfig() {
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
		config.CertConfig.CertPath = p2p.GetRealPath(config.CertConfig.CertPath)
		config.CertConfig.KeyPath = p2p.GetRealPath(config.CertConfig.KeyPath)
	}
	// nodeIP
	if config.NodeIP == "" {
		config.NodeIP = p2p.GetOutboundIP(config.DetectAddr).String()
	}
}

var config = p2p.DeployConfig{}

func initConfig() {
	viper.SetConfigFile(cfgFile)
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err != nil {
		log.Printf("Read file err! %s", err)
		return
	}
	if err := viper.Unmarshal(&config); err != nil {
		log.Printf("Unmarshal file err! %s", err)
		return
	}
	log.Println("Using config file:", viper.ConfigFileUsed())
}

func execute() {
	switch config.RunMode {
	case "root":
		log.Info("Run on P2P Root")
	case "agent":
		log.Info("Run on P2P Agent")
	}
	cache := p2p.NewCachePool(&p2p.CacheConfig{
		MaxEntry:   config.CacheConfig.MemCacheSize,
		CacheSize:  config.CacheConfig.FileCacheSize,
		CacheMedia: config.CacheConfig.FileCachePath,
	})
	hp := p2p.NewHostPicker(config.RootList, cache)
	fs := p2p.NewP2PFS(&p2p.FSConfig{
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
	serverHandler := p2p.NewP2PServer(&p2p.ServerConfig{
		MyAddr: myAddr,
		Fs:     fs,
		APIKey: "dadip2p",
	})
	var cert []tls.Certificate
	if config.CertConfig.CertEnable {
		cert = []tls.Certificate{*p2p.GetRootCA(config.CertConfig.CertPath, config.CertConfig.KeyPath, config.CertConfig.GenerateCert)}
	}
	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", config.Port),
		Handler:   serverHandler,
		TLSConfig: &tls.Config{Certificates: cert},
	}
	panic(server.ListenAndServe())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Execute failed! %s", err)
	}
}
