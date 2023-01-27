module github.com/ChainSafe/chainbridge-core-example

go 1.15

replace github.com/ChainSafe/chainbridge-core => github.com/simone1999/sygma-core v0.1.5

// replace github.com/ChainSafe/chainbridge-core => ../sygma-core

require (
	github.com/ChainSafe/chainbridge-celo-module v0.0.0-20220121131741-69b2ecf7dec5
	github.com/ChainSafe/chainbridge-core v0.0.0-20220120162654-c03a4d159125
	github.com/ethereum/go-ethereum v1.10.15
	github.com/rs/zerolog v1.29.0
	github.com/spf13/cobra v1.2.1
	github.com/spf13/viper v1.10.1
	github.com/stretchr/testify v1.7.0
	golang.org/x/crypto v0.0.0-20211215165025-cf75a172585e // indirect
)
