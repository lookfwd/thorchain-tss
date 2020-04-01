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
	logger             zerolog.Logger
	host               host.Host
	ceremonyLock       *sync.Mutex
	ceremonies         map[string]*Ceremony
	stopChan           chan struct{}
	timeout            time.Duration
	peersGroup         map[string]*PeerStatus
	joinPartyGroupLock *sync.Mutex
	threshold          int32
}

// NewPartyCoordinator create a new instance of PartyCoordinator
func NewPartyCoordinator(host host.Host, timeout time.Duration) *PartyCoordinator {
	pc := &PartyCoordinator{
		logger:             log.With().Str("module", "party_coordinator").Logger(),
		host:               host,
		ceremonyLock:       &sync.Mutex{},
		ceremonies:         make(map[string]*Ceremony),
		stopChan:           make(chan struct{}),
		timeout:            timeout,
		peersGroup:         make(map[string]PeerStatus),
		joinPartyGroupLock: &sync.Mutex{},
		threshold:          0,
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

	peerGroup, ok := pc.peersGroup[msg.ID]
	if !ok {
		pc.logger.Info().Msg("this party is not ready")
		return
	}
	newFound, err := peerGroup.updatePeer(remotePeer)
	if err != nil {
		pc.logger.Error().Err(err).Msg("receive msg from unknown peer")
		return
	}
	if newFound {
		peerGroup.newFound <- true
	}

	return
}

func (pc *PartyCoordinator) processJoinPartyRequest(remotePeer peer.ID, msg *messages.JoinPartyRequest) (*messages.JoinPartyResponse, error) {
	joinParty := NewJoinParty(msg, remotePeer)
	c, err := pc.onJoinParty(joinParty)
	if err != nil {
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
	if c == nil {
		// it only happen when the node was request to exit gracefully
		return &messages.JoinPartyResponse{
			ID:   msg.ID,
			Type: messages.JoinPartyResponse_Unknown,
		}, nil
	}

	select {
	case r := <-joinParty.Resp:
		return r, nil
	case onlinePeers := <-c.TimeoutChan:
		// make sure the ceremony get removed when there is timeout
		defer pc.ensureRemoveCeremony(msg.ID)
		return &messages.JoinPartyResponse{
			ID:      msg.ID,
			Type:    messages.JoinPartyResponse_Timeout,
			PeerIDs: onlinePeers,
		}, nil
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
func (pc *PartyCoordinator) onJoinParty(joinParty *JoinParty) (*Ceremony, error) {
	pc.logger.Info().
		Str("ID", joinParty.Msg.ID).
		Str("remote peer", joinParty.Peer.String()).
		Msgf("get join party request")
	pc.ceremonyLock.Lock()
	defer pc.ceremonyLock.Unlock()
	c, ok := pc.ceremonies[joinParty.Msg.ID]
	if !ok {
		return nil, errLeaderNotReady
	}
	if !c.ValidPeer(joinParty.Peer) {
		return nil, errUnknownPeer
	}
	if c.IsPartyExist(joinParty.Peer) {
		return nil, errUnknownPeer
	}
	c.JoinPartyRequests = append(c.JoinPartyRequests, joinParty)
	if !c.IsReady() {
		// Ceremony is not ready , still waiting for more party to join
		return c, nil
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
			return nil, nil
		case item.Resp <- resp:
		}
	}
	delete(pc.ceremonies, c.ID)
	return c, nil
}

func (pc *PartyCoordinator) ensureRemoveCeremony(messageID string) {
	pc.ceremonyLock.Lock()
	defer pc.ceremonyLock.Unlock()
	delete(pc.ceremonies, messageID)
}

func (pc *PartyCoordinator) removePeerGroup(messageID string) {
	pc.joinPartyGroupLock.Lock()
	defer pc.joinPartyGroupLock.Unlock()
	delete(pc.peersGroup, messageID)
}

func (pc *PartyCoordinator) createJoinPartyGroups(messageID string, peers []string, threshold int32) (*PeerStatus, error) {

	pIDs, err := pc.getPeerIDs(peers)
	if err != nil {
		pc.logger.Error().Err(err).Msg("fail to parse peer id")
		return nil, err
	}
	pc.threshold = threshold
	peerStatus := NewPeerStatus(pIDs, pc.host.ID())
	pc.joinPartyGroupLock.Lock()
	pc.peersGroup[messageID] = &peerStatus
	pc.joinPartyGroupLock.Unlock()
	return &peerStatus, nil
}

func (pc *PartyCoordinator) createCeremony(messageID string, peers []string, threshold int32) {
	pc.ceremonyLock.Lock()
	defer pc.ceremonyLock.Unlock()
	pIDs, err := pc.getPeerIDs(peers)
	if err != nil {
		pc.logger.Error().Err(err).Msg("fail to parse peer id")
	}
	pc.ceremonies[messageID] = NewCeremony(messageID, uint32(threshold), pIDs, pc.timeout)
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

func (pc *PartyCoordinator) sendRequestToAll(msg *messages.JoinPartyRequest, peers []peer.ID) {
	var wg sync.WaitGroup
	wg.Add(len(peers))
	for _, el := range peers {
		go func(peer peer.ID) {
			defer wg.Done()
			_, err := pc.sendRequestToPeer(msg, peer)
			if err != nil {
				pc.logger.Error().Err(err).Msg("error in send the join party request to peer")
			}
		}(el)
	}
	wg.Wait()
}

func (pc *PartyCoordinator) sendRequestToPeer(msg *messages.JoinPartyRequest, remotePeer peer.ID) (bool, error) {

	msgBuf, err := proto.Marshal(msg)
	if err != nil {
		return false, fmt.Errorf("fail to marshal msg to bytes: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()
	stream, err := pc.host.NewStream(ctx, remotePeer, joinPartyProtocol)
	if err != nil {
		return false, fmt.Errorf("fail to create stream to peer(%s):%w", remotePeer, err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			pc.logger.Error().Err(err).Msg("fail to close stream")
		}
	}()
	pc.logger.Info().Msgf("open stream to (%s) successfully", remotePeer)

	err = WriteStreamWithBuffer(msgBuf, stream)
	if err != nil {
		if errReset := stream.Reset(); errReset != nil {
			return false, errReset
		}
		return false, fmt.Errorf("fail to write message to stream:%w", err)
	}

	return false, nil
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
	// because the test stream doesn't support deadline
	if ApplyDeadline {
		// set a read deadline here , in case the coordinator doesn't timeout appropriately , and keep client hanging there
		timeout := pc.timeout + time.Second
		if err := stream.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, fmt.Errorf("fail to set read deadline")
		}
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
func (pc *PartyCoordinator) JoinPartyWithRetry(msg *messages.JoinPartyRequest, peers []string, threshold int32) ([]peer.ID, error) {
	peerGroup, err := pc.createJoinPartyGroups(msg.ID, peers, threshold)
	if err != nil {
		pc.logger.Error().Err(err).Msg("fail to create the join party group")
		return nil, err
	}
	defer pc.removePeerGroup(msg.ID)
	_, offline := peerGroup.getPeersStatus()

	bf := backoff.NewExponentialBackOff()
	bf.MaxElapsedTime = pc.timeout
	var wg sync.WaitGroup
	go func() {
		wg.Add(1)
		defer wg.Done()
		pc.sendRequestToAll(msg, offline)
	}()

	err = backoff.Retry(func() error {
		ret := peerGroup.getCoordinationStatus()
		if ret {
			return nil
		}
		select {
		case <-peerGroup.newFound:
			pc.logger.Info().Msg("we have found the new peer, reset the backoff timer")
			bf.Reset()
		default:
			pc.logger.Debug().Msg("no new peer found")
		}
		return errors.New("not all party are ready")
	}, bf)

	wg.Wait()
	onlinePeers, _ := peerGroup.getPeersStatus()
	pc.sendRequestToAll(msg, onlinePeers)

	//we always set ourselves as online
	onlinePeers = append(onlinePeers, pc.host.ID())
	if len(onlinePeers) == len(peers) {

		return onlinePeers, nil
	}
	return onlinePeers, err
}
