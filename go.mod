module github.com/EasterCompany/dex-event-service

go 1.25.6

require (
	github.com/EasterCompany/dex-go-utils v0.0.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/mux v1.8.1
	github.com/redis/go-redis/v9 v9.17.3
	golang.org/x/image v0.35.0
)

replace github.com/EasterCompany/dex-go-utils => ../dex-go-utils

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
)
