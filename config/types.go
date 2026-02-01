package config

import (
	sharedConfig "github.com/EasterCompany/dex-go-utils/config"
)

// Aliases to shared types in dex-go-utils
type ServiceMapConfig = sharedConfig.ServiceMapConfig
type ServiceType = sharedConfig.ServiceType
type ServiceEntry = sharedConfig.ServiceEntry
type ServiceCredentials = sharedConfig.ServiceCredentials
type OptionsConfig = sharedConfig.OptionsConfig
type CognitiveOptions = sharedConfig.CognitiveOptions
type DiscordOptions = sharedConfig.DiscordOptions
type SystemConfig = sharedConfig.SystemConfig
type CPUInfo = sharedConfig.CPUInfo
type GPUInfo = sharedConfig.GPUInfo
type StorageInfo = sharedConfig.StorageInfo
type PackageInfo = sharedConfig.PackageInfo
type ServerMapConfig = sharedConfig.ServerMapConfig
type Server = sharedConfig.Server

// GetSanitized is now a method on the shared type (aliased)
// If we need custom behavior we can keep it here but usually it matches.
