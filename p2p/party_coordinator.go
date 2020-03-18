package p2p

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gogo/protobuf/proto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-yamux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/tss/go-tss/messages"
)

type PartyCoordinator struct {
	logger       zerolog.Logger
	host         host.Host
	ceremonyLock *sync.Mutex
	ceremonies   map[string]*Ceremony
	stopChan     chan struct{}
	timeout      time.Duration
}

// NewPartyCoordinator create a new instance of PartyCoordinator
func NewPartyCoordinator(host host.Host, timeout time.Duration) *PartyCoordinator {
	pc := &PartyCoordinator{
		logger:       log.With().Str("module", "party_coordinator").Logger(),
		host:         host,
		ceremonyLock: &sync.Mutex{},
		ceremonies:   make(map[string]*Ceremony),
		stopChan:     make(chan struct{}),
		timeout:      timeout,
	}
	host.SetStreamHandler(joinPartyProtocol, pc.HandleStream)
	return pc
}

// Stop the PartyCoordinator rune
func (pc *PartyCoordinator) Stop() {
	defer pc.logger.Info().Msg("stop party coordinator")
	pc.host.RemoveStreamHandler(joinPartyProtocol)
	close(pc.stopChan)
}

// HandleStream handle party coordinate stream
func (pc *PartyCoordinator) HandleStream(stream network.Stream) {
	defer func() {
		if err := stream.Close(); err != nil {
			pc.logger.Err(err).Msg("fail to close the stream")
		}
	}()
	remotePeer := stream.Conn().RemotePeer()
	logger := pc.logger.With().Str("remote peer", remotePeer.String()).Logger()
	logger.Debug().Msg("reading from join party request")

	payload, err := ReadStreamWithBuffer(stream)
	if err != nil {
		logger.Err(err).Msgf("fail to read payload from stream")
		return
	}
	var msg messages.JoinPartyRequest
	if err := proto.Unmarshal(payload, &msg); err != nil {
		logger.Err(err).Msg("fail to unmarshal join party request")
		return
	}
	resp, err := pc.processJoinPartyRequest(remotePeer, &msg)
	if err != nil {
		logger.Error().Err(err).Msg("fail to process join party request")
		return
	}
	if err := pc.writeResponse(stream, resp); err != nil {
		logger.Error().Err(err).Msg("fail to write response to stream")
	}
}

func (pc *PartyCoordinator) processJoinPartyRequest(remotePeer peer.ID, msg *messages.JoinPartyRequest) (*messages.JoinPartyResponse, error) {
	joinParty := NewJoinParty(msg, remotePeer)
	if err := pc.onJoinParty(joinParty); err != nil {
		if errors.Is(err, errLeaderNotReady) {
			// leader node doesn't have request yet, so don't know how to handle the join party request
			return &messages.JoinPartyResponse{
				ID:   msg.ID,
				Type: messages.JoinPartyResponse_LeaderNotReady,
			}, nil
		}
		if errors.Is(err, errUnknownPeer) {
			return &messages.JoinPartyResponse{
				ID:   msg.ID,
				Type: messages.JoinPartyResponse_UnknownPeer,
			}, nil
		}
		pc.logger.Error().Err(err).Msg("fail to join party")
	}
	for {
		select {
		case r := <-joinParty.Resp:
			return r, nil
		case <-time.After(pc.timeout):
			// TODO make this timeout dynamic based on the threshold
			result, parties := pc.onJoinPartyTimeout(joinParty)
			if !result {
				return &messages.JoinPartyResponse{
					ID:      msg.ID,
					Type:    messages.JoinPartyResponse_Timeout,
					PeerIDs: parties,
				}, nil
			}
		}
	}
}

// writeResponse write the joinPartyResponse
func (pc *PartyCoordinator) writeResponse(stream network.Stream, resp *messages.JoinPartyResponse) error {
	buf, err := proto.Marshal(resp)
	if err != nil {
		return fmt.Errorf("fail to marshal resp to byte: %w", err)
	}
	_, err = stream.Write(buf)
	if err != nil {
		// when fail to write to the stream we shall reset it
		if resetErr := stream.Reset(); resetErr != nil {
			return fmt.Errorf("fail to reset the stream: %w", err)
		}
		return fmt.Errorf("fail to write response to stream: %w", err)
	}
	return nil
}

var (
	errLeaderNotReady = errors.New("leader node is not ready")
	errUnknownPeer    = errors.New("unknown peer trying to join party")
)

// onJoinParty is a call back function
func (pc *PartyCoordinator) onJoinParty(joinParty *JoinParty) error {
	pc.logger.Info().
		Str("ID", joinParty.Msg.ID).
		Str("remote peer", joinParty.Peer.String()).
		Msgf("get join party request")
	pc.ceremonyLock.Lock()
	defer pc.ceremonyLock.Unlock()
	c, ok := pc.ceremonies[joinParty.Msg.ID]
	if !ok {
		return errLeaderNotReady
	}
	if !c.ValidPeer(joinParty.Peer) {
		return errUnknownPeer
	}
	c.JoinPartyRequests = append(c.JoinPartyRequests, joinParty)
	if !c.IsReady() {
		// Ceremony is not ready , still waiting for more party to join
		return nil
	}

	resp := &messages.JoinPartyResponse{
		ID:      c.ID,
		Type:    messages.JoinPartyResponse_Success,
		PeerIDs: c.GetParties(),
	}
	pc.logger.Info().Msgf("party formed: %+v", resp.PeerIDs)
	for _, item := range c.JoinPartyRequests {
		select {
		case <-pc.stopChan: // receive request to exit
			return nil
		case item.Resp <- resp:
		}
	}
	delete(pc.ceremonies, c.ID)
	return nil
}

// onJoinPartyTimeout this method is to deal with the follow scenario
// the join party request had been waiting for a while(WaitForPartyGatheringTimeout)
// but it doesn't get enough nodes to start the ceremony , thus it trying to withdraw it's request
// the first bool return value indicate whether it should give up sending the timeout resp back to client
// usually that means a timeout and success has almost step on each other's foot, it should give up timeout , because the
// success resp is already there
func (pc *PartyCoordinator) onJoinPartyTimeout(joinParty *JoinParty) (bool, []string) {
	pc.logger.Info().
		Str("ID", joinParty.Msg.ID).
		Str("remote peer", joinParty.Peer.String()).
		Msgf("join party timeout")
	pc.ceremonyLock.Lock()
	defer pc.ceremonyLock.Unlock()
	c, ok := pc.ceremonies[joinParty.Msg.ID]
	if !ok {
		return false, nil
	}

	pc.logger.Info().Msgf("join party timeout:%+v,requests:%+v", c.Peers, c.JoinPartyRequests)
	// it could be timeout / finish almost happen at the same time, we give up timeout
	if c.Status == Finished {
		return true, c.GetParties()
	}
	// remove this party as they sick of waiting
	idxToDelete := -1
	for idx, p := range c.JoinPartyRequests {
		if p.Peer == joinParty.Peer {
			idxToDelete = idx
		}
	}
	// withdraw request
	if idxToDelete != -1 {
		c.JoinPartyRequests = append(c.JoinPartyRequests[:idxToDelete], c.JoinPartyRequests[idxToDelete+1:]...)
	}
	// no one is waiting , let's remove the ceremony
	if len(c.JoinPartyRequests) == 0 {
		delete(pc.ceremonies, joinParty.Msg.ID)
	}
	return false, c.GetParties()
}

func (pc *PartyCoordinator) createCeremony(messageID string, peers []string, threshold int32) {
	pc.ceremonyLock.Lock()
	defer pc.ceremonyLock.Unlock()
	pIDs, err := pc.getPeerIDs(peers)
	if err != nil {
		pc.logger.Error().Err(err).Msg("fail to parse peer id")
	}
	ceremony := &Ceremony{
		ID:                messageID,
		Threshold:         uint32(threshold),
		JoinPartyRequests: []*JoinParty{},
		Status:            GatheringParties,
		Peers:             pIDs,
	}
	pc.ceremonies[messageID] = ceremony
}

func (pc *PartyCoordinator) getPeerIDs(ids []string) ([]peer.ID, error) {
	result := make([]peer.ID, len(ids))
	for i, item := range ids {
		pid, err := peer.Decode(item)
		if err != nil {
			return nil, fmt.Errorf("fail to decode peer id(%s):%w", item, err)
		}
		result[i] = pid
	}
	return result, nil
}

// JoinParty join a ceremony , it could be keygen or key sign
func (pc *PartyCoordinator) JoinParty(remotePeer peer.ID, msg *messages.JoinPartyRequest, peers []string, threshold int32) (*messages.JoinPartyResponse, error) {
	if remotePeer == pc.host.ID() {
		pc.logger.Info().
			Str("message-id", msg.ID).
			Str("peerid", remotePeer.String()).
			Int32("threshold", threshold).
			Msg("we are the leader, create ceremony")
		pc.createCeremony(msg.ID, peers, threshold)
		return pc.processJoinPartyRequest(remotePeer, msg)
	}
	msgBuf, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("fail to marshal msg to bytes: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()
	stream, err := pc.host.NewStream(ctx, remotePeer, joinPartyProtocol)
	if err != nil {
		return nil, fmt.Errorf("fail to create stream to peer(%s):%w", remotePeer, err)
	}
	pc.logger.Info().Msgf("open stream to (%s) successfully", remotePeer)
	defer func() {
		if err := stream.Close(); err != nil {
			pc.logger.Error().Err(err).Msg("fail to close stream")
		}
	}()
	err = WriteStreamWithBuffer(msgBuf, stream)
	if err != nil {
		if errReset := stream.Reset(); errReset != nil {
			return nil, errReset
		}
		return nil, fmt.Errorf("fail to write message to stream:%w", err)
	}
	// read response
	respBuf, err := ioutil.ReadAll(stream)
	if err != nil {
		if err != yamux.ErrConnectionReset {
			return nil, fmt.Errorf("fail to read response: %w", err)
		}
	}
	if len(respBuf) == 0 {
		return nil, errors.New("fail to get response")
	}
	var resp messages.JoinPartyResponse
	if err := proto.Unmarshal(respBuf, &resp); err != nil {
		return nil, fmt.Errorf("fail to unmarshal JoinGameResp: %w", err)
	}
	return &resp, nil
}

// JoinPartyWithRetry this method provide the functionality to join party with retry and backoff
func (pc *PartyCoordinator) JoinPartyWithRetry(remotePeer peer.ID, msg *messages.JoinPartyRequest, peers []string, threshold int32) (*messages.JoinPartyResponse, error) {
	bf := backoff.NewExponentialBackOff()
	bf.MaxElapsedTime = pc.timeout
	resp := &messages.JoinPartyResponse{
		Type: messages.JoinPartyResponse_Unknown,
	}
	err := backoff.Retry(func() error {
		joinPartyResp, err := pc.JoinParty(remotePeer, msg, peers, threshold)
		if err == nil {
			if joinPartyResp.Type == messages.JoinPartyResponse_LeaderNotReady {
				return errors.New("leader not ready")
			}
			resp = joinPartyResp
			return nil
		}
		pc.logger.Err(err).Msg("fail to join party")
		return err
	}, bf)
	if err != nil {
		pc.logger.Error().Err(err).Msgf("fail to join party after maximum retry:%s", pc.timeout)
	}
	return resp, err
}
