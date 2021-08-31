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
	LogLevel    string
	APIKey      string
	ProxyConfig ProxyConfig
	P2PConfig   P2PConfig
}

type ProxyConfig struct {
	Port       int
	ProxyHTTPS bool
	CertConfig CertConfig
}

type P2PConfig struct {
	RunMode        string
	RootList       []string
	NodeIP         string
	DetectAddr     string
	ServeBySSL     bool
	Port           int
	MyAddr         string // not in yaml
	CacheConfig    CacheConfig
	PrefetchConfig PrefetchConfig
}

type CertConfig struct {
	GenerateCert bool // not in yaml
	CertPath     string
	KeyPath      string
}

type CacheConfig struct {
	FileCacheSize int64
	FileCachePath string
	MemCacheSize  int64
}

type PrefetchConfig struct {
	PrefetchEnable bool
	PrefetchThread int
}

// CheckConfig used to check config and fix some configuration
func CheckConfig(config *DeployConfig) {
	// deploy
	{
		// log level
		level, err := log.ParseLevel(config.LogLevel)
		if err != nil {
			log.Warnf("Log level %s parse fail! Use info level instead.", config.LogLevel)
			level = log.InfoLevel
		}
		log.SetLevel(level)
	}
	// proxy
	{
		// cert
		if config.ProxyConfig.ProxyHTTPS {
			config.ProxyConfig.CertConfig.CertPath = util.GetRealPath(config.ProxyConfig.CertConfig.CertPath)
			config.ProxyConfig.CertConfig.KeyPath = util.GetRealPath(config.ProxyConfig.CertConfig.KeyPath)
		}
	}
	// p2p
	{
		// run mode
		if config.P2PConfig.RunMode != "root" && config.P2PConfig.RunMode != "agent" {
			log.Fatalf("Unexpected run mode %s!", config.P2PConfig.RunMode)
		}
		// prefetch
		if !config.P2PConfig.PrefetchConfig.PrefetchEnable {
			config.P2PConfig.PrefetchConfig.PrefetchThread = 0
		}
		// cache
		config.P2PConfig.CacheConfig.FileCachePath = util.GetRealPath(config.P2PConfig.CacheConfig.FileCachePath)
		// nodeIP
		if config.P2PConfig.NodeIP == "" {
			config.P2PConfig.NodeIP = util.GetOutboundIP(config.P2PConfig.DetectAddr).String()
		}
		// my addr
		if !config.P2PConfig.ServeBySSL {
			config.P2PConfig.MyAddr = fmt.Sprintf("http://%s:%d", config.P2PConfig.NodeIP, config.P2PConfig.Port)
		} else {
			config.P2PConfig.MyAddr = fmt.Sprintf("https://%s:%d", config.P2PConfig.NodeIP, config.P2PConfig.Port)
		}
		// root list
		for idx := range config.P2PConfig.RootList {
			if !config.P2PConfig.ServeBySSL {
				config.P2PConfig.RootList[idx] = fmt.Sprintf("http://%s", config.P2PConfig.RootList[idx])
			} else {
				config.P2PConfig.RootList[idx] = fmt.Sprintf("https://%s", config.P2PConfig.RootList[idx])
			}
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
