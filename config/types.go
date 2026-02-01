package config

import (
	sharedConfig "github.com/EasterCompany/dex-go-utils/config"
)

// Aliases to shared types in dex-go-utils for backward compatibility
type ServiceMapConfig = sharedConfig.ServiceMapConfig
type ServiceType = sharedConfig.ServiceType
type ServiceEntry = sharedConfig.ServiceEntry
type ServiceCredentials = sharedConfig.ServiceCredentials
type OptionsConfig = sharedConfig.OptionsConfig
type CognitiveOptions = sharedConfig.CognitiveOptions
type DiscordOptions = sharedConfig.DiscordOptions
type RoleConfig = sharedConfig.RoleConfig
type SystemConfig = sharedConfig.SystemConfig
type ServerMapConfig = sharedConfig.ServerMapConfig
type Server = sharedConfig.Server
