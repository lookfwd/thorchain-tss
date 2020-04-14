package common

import (
	"fmt"
	"sort"

	bkg "github.com/binance-chain/tss-lib/ecdsa/keygen"
	btss "github.com/binance-chain/tss-lib/tss"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/tendermint/tendermint/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/tss/go-tss/messages"
)

type tssHelpSuite struct {
	tssCommon *TssCommon
}

var testPeers = []string{"16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh",
	"16Uiu2HAm2FzqoUdS6Y9Esg2EaGcAG5rVe1r6BFNnmmQr2H3bqafa",
	"16Uiu2HAmACG5DtqmQsHtXg4G2sLS65ttv84e7MrL4kapkjfmhxAp",
	"16Uiu2HAmAWKWf5vnpiAhfdSQebTbbB3Bg35qtyG7Hr4ce23VFA8V"}

var _ = Suite(&tssHelpSuite{})

func (t *tssHelpSuite) SetUpTest(c *C) {

	SetupBech32Prefix()
	broadcast := make(chan *messages.BroadcastMsgChan)
	conf := TssConfig{}
	sk := secp256k1.GenPrivKey()
	tssCommon := NewTssCommon("123", broadcast, conf, "testID", sk)
	p1, err := peer.Decode(testPeers[0])
	c.Assert(err, IsNil)
	p2, err := peer.Decode(testPeers[1])
	c.Assert(err, IsNil)
	p3, err := peer.Decode(testPeers[2])
	c.Assert(err, IsNil)
	tssCommon.lastUnicastPeer["testType"] = []peer.ID{p1, p2, p3}
	localTestPubKeys := testPubKeys[:]
	sort.Strings(localTestPubKeys)
	partiesID, localPartyID, err := GetParties(localTestPubKeys, testPubKeys[0])
	c.Assert(err, IsNil)
	partyIDMap := SetupPartyIDMap(partiesID)
	err = SetupIDMaps(partyIDMap, tssCommon.PartyIDtoP2PID)
	outCh := make(chan btss.Message, len(partiesID))
	endCh := make(chan bkg.LocalPartySaveData, len(partiesID))
	ctx := btss.NewPeerContext(partiesID)
	params := btss.NewParameters(ctx, localPartyID, len(partiesID), 3)
	keyGenParty := bkg.NewLocalParty(params, outCh, endCh)
	tssCommon.SetPartyInfo(&PartyInfo{
		Party:      keyGenParty,
		PartyIDMap: partyIDMap,
	})
	t.tssCommon = tssCommon
}

func (t *tssHelpSuite) TestGetHashToBroadcast(c *C) {
	testMap := make(map[string]string)
	val, freq, err := getHighestFreq(testMap)
	c.Assert(err, NotNil)
	val, freq, err = getHighestFreq(nil)
	c.Assert(err, NotNil)
	testMap["1"] = "aa"
	testMap["2"] = "aa"
	testMap["3"] = "aa"
	testMap["4"] = "ab"
	testMap["5"] = "bb"
	testMap["6"] = "bb"
	testMap["7"] = "bc"
	testMap["8"] = "cd"
	val, freq, err = getHighestFreq(testMap)
	c.Assert(err, IsNil)
	c.Assert(val, Equals, "aa")
	c.Assert(freq, Equals, 3)

}

func (t *tssHelpSuite) TestGetUnicastBlame(c *C) {

	_, err := t.tssCommon.GetUnicastBlame("testTypeWrong")
	c.Assert(err, NotNil)
	blame, err := t.tssCommon.GetUnicastBlame("testType")
	c.Assert(err, IsNil)
	//c.Assert(blame[0].Pubkey, Equals, testPubKeys[3])
	fmt.Println(blame)

}

func (t *tssHelpSuite) TestBroadcastBlame(c *C) {

	pi := t.tssCommon.getPartyInfo()

	r1 := btss.MessageRouting{
		From:                    pi.PartyIDMap["1"],
		To:                      nil,
		IsBroadcast:             false,
		IsToOldCommittee:        false,
		IsToOldAndNewCommittees: false,
	}
	msg := messages.WireMessage{
		Routing:   &r1,
		RoundInfo: "key1",
		Message:   nil,
	}

	t.tssCommon.msgStored.storeTssMsg("key1", &msg)
	blames, err := t.tssCommon.GetBroadcastBlame("key1")
	c.Assert(err, IsNil)
	var blamePubKeys []string
	for _, el := range blames {
		blamePubKeys = append(blamePubKeys, el.Pubkey)
	}
	sort.Strings(blamePubKeys)
	expected := testPubKeys[2:]
	sort.Strings(expected)
	c.Assert(blamePubKeys, DeepEquals, expected)
}
