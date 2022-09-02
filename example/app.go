// Copyright 2021 ChainSafe Systems
// SPDX-License-Identifier: LGPL-3.0-only

package example

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/ChainSafe/chainbridge-celo-module/transaction"
	"github.com/ChainSafe/chainbridge-core/chains/evm"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/bridge"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmclient"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmgaspricer"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/transactor/signAndSend"
	"github.com/ChainSafe/chainbridge-core/chains/evm/listener"
	"github.com/ChainSafe/chainbridge-core/chains/evm/voter"
	"github.com/ChainSafe/chainbridge-core/config"
	"github.com/ChainSafe/chainbridge-core/config/chain"
	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/ChainSafe/chainbridge-core/lvldb"
	"github.com/ChainSafe/chainbridge-core/opentelemetry"
	"github.com/ChainSafe/chainbridge-core/relayer"
	"github.com/ChainSafe/chainbridge-core/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

func Run() error {
	errChn := make(chan error)
	stopChn := make(chan struct{})

	configuration, err := config.GetConfig(viper.GetString(flags.ConfigFlagName))
	db, err := lvldb.NewLvlDB(viper.GetString(flags.BlockstoreFlagName))
	if err != nil {
		panic(err)
	}
	blockstore := store.NewBlockStore(db)

	chains := []relayer.RelayedChain{}
	for _, chainConfig := range configuration.ChainConfigs {
		switch chainConfig["type"] {
		case "evm", "celo":
			{
				evmConfig, err := chain.NewEVMConfig(chainConfig)
				if err != nil {
					panic(err)
				}
				client, err := evmclient.NewEVMClient(evmConfig)
				if err != nil {
					panic(err)
				}
				gasPricer := evmgaspricer.NewStaticGasPriceDeterminant(client, nil)
				t := signAndSend.NewSignAndSendTransactor(transaction.NewCeloTransaction, gasPricer, client)
				bridgeContract := bridge.NewBridgeContract(client, common.HexToAddress(evmConfig.Bridge), t)

				eventHandler := listener.NewETHEventHandler(*bridgeContract)
				mh := voter.NewEVMMessageHandler(*bridgeContract)

				for _, erc20HandlerContract := range evmConfig.Erc20Handlers {
					eventHandler.RegisterEventHandler(erc20HandlerContract, listener.Erc20EventHandler)
					mh.RegisterMessageHandler(erc20HandlerContract, voter.ERC20MessageHandler)
				}
				for _, erc721HandlerContract := range evmConfig.Erc721Handlers {
					eventHandler.RegisterEventHandler(erc721HandlerContract, listener.Erc721EventHandler)
					mh.RegisterMessageHandler(erc721HandlerContract, voter.ERC721MessageHandler)
				}
				for _, genericHandlerContract := range evmConfig.GenericHandlers {
					eventHandler.RegisterEventHandler(genericHandlerContract, listener.GenericEventHandler)
					mh.RegisterMessageHandler(genericHandlerContract, voter.GenericMessageHandler)
				}

				evmListener := listener.NewEVMListener(client, eventHandler, common.HexToAddress(evmConfig.Bridge))
				evmVoter := voter.NewVoter(mh, client, bridgeContract)
				chains = append(chains, evm.NewEVMChain(evmListener, evmVoter, blockstore, evmConfig))
			}
		}
	}

	r := relayer.NewRelayer(chains, &opentelemetry.ConsoleTelemetry{})
	go r.Start(stopChn, errChn)

	sysErr := make(chan os.Signal, 1)
	signal.Notify(sysErr,
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGHUP,
		syscall.SIGQUIT)

	select {
	case err := <-errChn:
		log.Error().Err(err).Msg("failed to listen and serve")
		close(stopChn)
		return err
	case sig := <-sysErr:
		log.Info().Msgf("terminating got [%v] signal", sig)
		return nil
	}
}
