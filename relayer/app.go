// Copyright 2021 ChainSafe Systems
// SPDX-License-Identifier: LGPL-3.0-only

package relayer

import (
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/bridge"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmclient"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmgaspricer"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmtransaction"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/transactor"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/transactor/signAndSend"
	"github.com/ChainSafe/chainbridge-core/chains/evm/listener"
	"github.com/ChainSafe/chainbridge-core/chains/evm/voter"
	"github.com/ChainSafe/chainbridge-core/config/chain"
	"github.com/ChainSafe/chainbridge-core/relayer/messageprocessors"
	"github.com/ethereum/go-ethereum/common"
	"os"
	"os/signal"
	"syscall"

	"github.com/ChainSafe/chainbridge-core/chains/evm"
	"github.com/ChainSafe/chainbridge-core/config"
	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/ChainSafe/chainbridge-core/lvldb"
	"github.com/ChainSafe/chainbridge-core/opentelemetry"
	"github.com/ChainSafe/chainbridge-core/relayer"
	"github.com/ChainSafe/chainbridge-core/store"
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
	clients := map[uint8]calls.ContractCallerDispatcher{}
	transactors := map[uint8]transactor.Transactor{}
	evmConfigs := map[uint8]*chain.EVMConfig{}
	for _, chainConfig := range configuration.ChainConfigs {
		switch chainConfig["type"] {
		case "evm":
			{
				/*
					chain, err := evm.SetupDefaultEVMChain(chainConfig, evmtransaction.NewTransaction, blockstore)
					if err != nil {
						panic(err)
					}

					chains = append(chains, chain)
				*/

				evmConfig, err := chain.NewEVMConfig(chainConfig)
				if err != nil {
					panic(err)
				}

				client, err := evmclient.NewEVMClient(evmConfig)
				if err != nil {
					panic(err)
				}
				gasPricer := evmgaspricer.NewLondonGasPriceClient(client, nil)
				t := signAndSend.NewSignAndSendTransactor(evmtransaction.NewTransaction, gasPricer, client)
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

				var evmVoter *voter.EVMVoter
				evmVoter, err = voter.NewVoterWithSubscription(mh, client, bridgeContract)
				if err != nil {
					log.Error().Msgf("failed creating voter with subscription: %s. Falling back to default voter.", err.Error())
					evmVoter = voter.NewVoter(mh, client, bridgeContract)
				}

				//var evmVoter *sqs_voter.EVMProposerSQS
				//evmVoter, err = sqs_voter.NewVoter(mh, client, bridgeContract, *evmConfig.GeneralChainConfig.Id, "IcecreamBridgeRelayer1Proposals")
				//if err != nil {
				//	panic(err)
				//}

				chains = append(chains, evm.NewEVMChain(evmListener, evmVoter, blockstore, evmConfig))
				clients[*evmConfig.GeneralChainConfig.Id] = client
				transactors[*evmConfig.GeneralChainConfig.Id] = t
				evmConfigs[*evmConfig.GeneralChainConfig.Id] = evmConfig
			}
		default:
			panic("invalid chain type")
		}
	}

	// adjustment for tokens with different decimals across chains. Automatically gets decimals over RPC from token contract
	messageProcessorDecimals := messageprocessors.AdjustDecimalsForERC20AmountMessageAutoProcessor(clients, transactors, evmConfigs)

	r := relayer.NewRelayer(chains, &opentelemetry.ConsoleTelemetry{}, messageProcessorDecimals)
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
