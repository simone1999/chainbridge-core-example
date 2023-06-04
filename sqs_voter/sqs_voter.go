package sqs_voter

import (
	"encoding/hex"
	"encoding/json"
	"github.com/ChainSafe/chainbridge-core/chains/evm/voter"
	"github.com/ChainSafe/chainbridge-core/chains/evm/voter/proposal"
	"github.com/ChainSafe/chainbridge-core/relayer/message"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"
	"math/rand"
	"time"
)

const (
	maxSimulateVoteChecks = 5
	maxShouldVoteChecks   = 40
	shouldVoteCheckPeriod = 15
)

var (
	Sleep = time.Sleep
)

type EVMProposerSQS struct {
	mh                  voter.MessageHandler
	client              voter.ChainClient
	bridgeContract      voter.BridgeContract
	sqsClient           sqs.SQS
	sqsQueueURL         string
	destinationDomainID uint8
}

type SQSProposal struct {
	SourceDomainId      uint8
	DestinationDomainId uint8
	DepositNonce        uint64
	ResourceId          common.Hash
	Data                string
	DepositTxHash       common.Hash
	DepositBlock        uint64
	BridgeAddress       common.Address
	HandlerAddress      common.Address
	RelayerAddress      common.Address
}

// NewVoter creates an instance of EVMProposerSQS that proposes votes to AWS SQS.
func NewVoter(mh voter.MessageHandler, client voter.ChainClient, bridgeContract voter.BridgeContract, destinationdomainId uint8, sqsQueueName string) (*EVMProposerSQS, error) {
	awsSess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))
	sqsClient := sqs.New(awsSess)

	result, err := sqsClient.GetQueueUrl(&sqs.GetQueueUrlInput{
		QueueName: &sqsQueueName,
	})
	if err != nil {
		return nil, err
	}

	return &EVMProposerSQS{
		mh:                  mh,
		client:              client,
		bridgeContract:      bridgeContract,
		sqsClient:           *sqsClient,
		sqsQueueURL:         *result.QueueUrl,
		destinationDomainID: destinationdomainId,
	}, nil
}

// VoteProposal checks if relayer already voted and is threshold
// satisfied and propose a vote to SQS if it isn't.
func (v *EVMProposerSQS) VoteProposal(m *message.Message) error {
	prop, err := v.mh.HandleMessage(m)
	if err != nil {
		return err
	}

	votedByTheRelayer, err := v.bridgeContract.IsProposalVotedBy(v.client.RelayerAddress(), prop)
	if err != nil {
		return err
	}
	if votedByTheRelayer {
		return nil
	}

	shouldVote, err := v.shouldVoteForProposal(prop, 0)
	if err != nil {
		log.Error().Err(err)
		return err
	}

	if !shouldVote {
		log.Debug().Msgf("Proposal %+v already satisfies threshold", prop)
		return nil
	}
	err = v.repetitiveSimulateVote(prop, 0)
	if err != nil {
		log.Error().Err(err)
		return err
	}

	sqsProposal := SQSProposal{
		SourceDomainId:      prop.Source,
		DestinationDomainId: v.destinationDomainID,
		DepositNonce:        prop.DepositNonce,
		ResourceId:          common.Hash(prop.ResourceId),
		Data:                hex.EncodeToString(prop.Data),
		DepositTxHash:       prop.DepositTxHash,
		DepositBlock:        prop.DepositBlock,
		BridgeAddress:       prop.BridgeAddress,
		HandlerAddress:      prop.HandlerAddress,
		RelayerAddress:      v.client.RelayerAddress(),
	}

	propJsonBytes, err := json.Marshal(sqsProposal)
	if err != nil {
		log.Error().Err(err)
		return err
	}
	propJson := string(propJsonBytes)

	_, err = v.sqsClient.SendMessage(&sqs.SendMessageInput{
		MessageBody: aws.String(propJson),
		QueueUrl:    &v.sqsQueueURL,
	})
	if err != nil {
		log.Error().Err(err)
		return err
	}
	log.Info().Uint64("nonce", prop.DepositNonce).Msgf("Sent vote proposal")

	/*
		hash, err := v.bridgeContract.VoteProposal(prop, transactor.TransactOptions{})
		if err != nil {
			return fmt.Errorf("voting failed. Err: %w", err)
		}

		log.Debug().Str("hash", hash.String()).Uint64("nonce", prop.DepositNonce).Msgf("Voted")
	*/

	return nil
}

// shouldVoteForProposal checks if proposal already has threshold with pending
// proposal votes from other relayers.
// Only works properly in conjuction with NewVoterWithSubscription as without a subscription
// no pending txs would be received and pending vote count would be 0.
func (v *EVMProposerSQS) shouldVoteForProposal(prop *proposal.Proposal, tries int) (bool, error) {
	// random delay to prevent all relayers checking for pending votes
	// at the same time and all of them sending another tx
	Sleep(time.Duration(rand.Intn(shouldVoteCheckPeriod)) * time.Second)

	ps, err := v.bridgeContract.ProposalStatus(prop)
	if err != nil {
		return false, err
	}

	if ps.Status == message.ProposalStatusExecuted || ps.Status == message.ProposalStatusCanceled {
		return false, nil
	}

	threshold, err := v.bridgeContract.GetThreshold()
	if err != nil {
		return false, err
	}

	if ps.YesVotesTotal >= threshold && tries < maxShouldVoteChecks {
		// Wait until proposal status is finalized to prevent missing votes
		// in case of dropped txs
		tries++
		return v.shouldVoteForProposal(prop, tries)
	}

	return true, nil
}

// repetitiveSimulateVote repeatedly tries(5 times) to simulate vote proposal call until it succeeds
func (v *EVMProposerSQS) repetitiveSimulateVote(prop *proposal.Proposal, tries int) error {
	err := v.bridgeContract.SimulateVoteProposal(prop)
	if err != nil {
		if tries < maxSimulateVoteChecks {
			tries++
			return v.repetitiveSimulateVote(prop, tries)
		}
		return err
	} else {
		return nil
	}
}
