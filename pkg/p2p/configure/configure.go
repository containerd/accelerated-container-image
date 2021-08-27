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

package configure

import (
	"fmt"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// DeployConfig is server config
type DeployConfig struct {
	LogLevel       string
	RunMode        string
	RootList       []string
	NodeIP         string
	DetectAddr     string
	ServeBySSL     bool
	Port           int
	CertConfig     DeployCertConfig
	CacheConfig    DeployCacheConfig
	PrefetchConfig DeployPrefetchConfig
}

type DeployCertConfig struct {
	CertEnable   bool
	GenerateCert bool
	CertPath     string
	KeyPath      string
}

type DeployCacheConfig struct {
	FileCacheEnable bool
	FileCacheSize   int64
	FileCachePath   string
	MemCacheEnable  bool
	MemCacheSize    int64
}

type DeployPrefetchConfig struct {
	PrefetchEnable bool
	PrefetchThread int
}

// CheckConfig used to check config and fix some configuration
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
		config.CertConfig.CertPath = util.GetRealPath(config.CertConfig.CertPath)
		config.CertConfig.KeyPath = util.GetRealPath(config.CertConfig.KeyPath)
	}
	// cache media
	if config.CacheConfig.FileCacheEnable {
		config.CacheConfig.FileCachePath = util.GetRealPath(config.CacheConfig.FileCachePath)
	}
	// nodeIP
	if config.NodeIP == "" {
		config.NodeIP = util.GetOutboundIP(config.DetectAddr).String()
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

// InitConfig used to load config from disk
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
