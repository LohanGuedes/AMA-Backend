package gen

//go:generate go run ./cmd/tools/terndotenv
//go:generate sqlc generate -f ./internal/store/pgstore/sqlc.yaml
