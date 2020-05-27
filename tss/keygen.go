package tss

import (
	"sync/atomic"

	"github.com/libp2p/go-libp2p-core/protocol"

	"gitlab.com/thorchain/tss/go-tss/blame"
	"gitlab.com/thorchain/tss/go-tss/common"
	"gitlab.com/thorchain/tss/go-tss/keygen"
	"gitlab.com/thorchain/tss/go-tss/messages"
)

func (t *TssServer) Keygen(req keygen.Request) (keygen.Response, error) {
	t.tssKeyGenLocker.Lock()
	defer t.tssKeyGenLocker.Unlock()
	status := common.Success
	msgID, err := t.requestToMsgId(req)
	if err != nil {
		return keygen.Response{}, err
	}
	keygenInstance := keygen.NewTssKeyGen(
		t.p2pCommunication.GetLocalPeerID(),
		t.conf,
		t.localNodePubKey,
		t.p2pCommunication.BroadcastMsgChan,
		t.stopChan,
		t.preParams,
		msgID,
		t.stateManager,
		t.privateKey, "")

	keygenMsgChannel := keygenInstance.GetTssKeyGenChannels()
	t.p2pCommunication.SetSubscribe(messages.TSSKeyGenMsg, msgID, keygenMsgChannel)
	t.p2pCommunication.SetSubscribe(messages.TSSKeyGenVerMsg, msgID, keygenMsgChannel)
	t.p2pCommunication.SetSubscribe(messages.TSSControlMsg, msgID, keygenMsgChannel)
	t.p2pCommunication.SetSubscribe(messages.TSSTaskDone, msgID, keygenMsgChannel)

	defer t.p2pCommunication.CancelSubscribe(messages.TSSKeyGenMsg, msgID)
	defer t.p2pCommunication.CancelSubscribe(messages.TSSKeyGenVerMsg, msgID)
	defer t.p2pCommunication.CancelSubscribe(messages.TSSControlMsg, msgID)
	defer t.p2pCommunication.CancelSubscribe(messages.TSSTaskDone, msgID)

	onlinePeers, proto, err := t.joinParty(msgID, req.Keys, req.Protos)
	if err != nil {
		if onlinePeers == nil {
			t.logger.Error().Err(err).Msg("error before we start join party")
			return keygen.Response{
				Status: common.Fail,
				Blame:  blame.NewBlame(blame.InternalError, []blame.Node{}),
			}, nil
		}
		blameMgr := keygenInstance.GetTssCommonStruct().GetBlameMgr()
		blameNodes, err := blameMgr.NodeSyncBlame(req.Keys, onlinePeers)
		if err != nil {
			t.logger.Err(err).Msg("fail to get peers to blame")
		}
		// make sure we blame the leader as well
		t.logger.Error().Err(err).Msgf("fail to form keysign party with online:%v", onlinePeers)
		return keygen.Response{
			Status: common.Fail,
			Blame:  blameNodes,
		}, nil
	}
	found := false
	for _, el := range req.Protos {
		if el == protocol.ConvertToStrings([]protocol.ID{proto})[0] {
			found = true
			break
		}
	}
	if !found {
		t.logger.Error().Msgf("the protocol(%s) is not supported by this request", protocol.ConvertToStrings([]protocol.ID{proto}))
		return keygen.Response{
			Status: common.Fail,
			Blame:  blame.NewBlame(blame.UnsupportedProtocol, []blame.Node{}),
		}, nil
	}
	t.logger.Info().Msg("keygen party formed")
	keygenInstance.GetTssCommonStruct().SetProto(proto)
	// the statistic of keygen only care about Tss it self, even if the
	// following http response aborts, it still counted as a successful keygen
	// as the Tss model runs successfully.
	k, err := keygenInstance.GenerateNewKey(req)
	blameMgr := keygenInstance.GetTssCommonStruct().GetBlameMgr()
	if err != nil {
		atomic.AddUint64(&t.Status.FailedKeyGen, 1)
		t.logger.Error().Err(err).Msg("err in keygen")
		blameNodes := *blameMgr.GetBlame()
		return keygen.NewResponse("", "", common.Fail, blameNodes), err
	} else {
		atomic.AddUint64(&t.Status.SucKeyGen, 1)
	}

	newPubKey, addr, err := common.GetTssPubKey(k)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to generate the new Tss key")
		status = common.Fail
	}

	blameNodes := *blameMgr.GetBlame()
	return keygen.NewResponse(
		newPubKey,
		addr.String(),
		status,
		blameNodes,
	), nil
}
