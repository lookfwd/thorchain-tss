package keysign

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	bc "github.com/binance-chain/tss-lib/common"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/tendermint/btcd/btcec"
)

// Notifier
type Notifier struct {
	messageID  string
	messages   [][]byte // the message
	poolPubKey string
	resp       chan []*bc.SignatureData
}

// NewNotifier create a new instance of Notifier
func NewNotifier(messageID string, messages [][]byte, poolPubKey string) (*Notifier, error) {
	if len(messageID) == 0 {
		return nil, errors.New("messageID is empty")
	}
	if len(messages) == 0 {
		return nil, errors.New("message is nil")
	}
	if len(poolPubKey) == 0 {
		return nil, errors.New("pool pubkey is empty")
	}
	return &Notifier{
		messageID:  messageID,
		messages:   messages,
		poolPubKey: poolPubKey,
		resp:       make(chan []*bc.SignatureData, 1),
	}, nil
}

func (n *Notifier) verifySignature(sigs []*bc.SignatureData) (bool, error) {
	// we should be able to use any of the pubkeys to verify the signature
	pubKey, err := sdk.GetAccPubKeyBech32(n.poolPubKey)
	if err != nil {
		return false, fmt.Errorf("fail to get pubkey from bech32 pubkey string(%s):%w", n.poolPubKey, err)
	}
	if len(sigs) != len(n.messages) {
		return false, errors.New("message num and signature num does not match")
	}
	signatureMap := make(map[string]*bc.SignatureData)
	for _, el := range sigs {
		signatureMap[hex.EncodeToString(el.M)] = el
	}

	for _, el := range n.messages {
		signature, ok := signatureMap[hex.EncodeToString(el)]
		if !ok {
			return false, nil
		}
		ret := pubKey.VerifyBytes(el, n.getSignatureBytes(signature))
		if !ret {
			return ret, nil
		}
	}
	return true, nil
}

func (n *Notifier) getSignatureBytes(data *bc.SignatureData) []byte {
	R := new(big.Int).SetBytes(data.R)
	S := new(big.Int).SetBytes(data.S)
	N := btcec.S256().N
	halfOrder := new(big.Int).Rsh(N, 1)
	// see: https://github.com/ethereum/go-ethereum/blob/f9401ae011ddf7f8d2d95020b7446c17f8d98dc1/crypto/signature_nocgo.go#L90-L93
	if S.Cmp(halfOrder) == 1 {
		S.Sub(N, S)
	}

	// Serialize signature to R || S.
	// R, S are padded to 32 bytes respectively.
	rBytes := R.Bytes()
	sBytes := S.Bytes()

	sigBytes := make([]byte, 64)
	// 0 pad the byte arrays from the left if they aren't big enough.
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)
	return sigBytes
}

// ProcessSignature is to verify whether the signature is valid
// return value bool , true indicated we already gather all the signature from keysign party, and they are all match
// false means we are still waiting for more signature from keysign party
func (n *Notifier) ProcessSignature(data []*bc.SignatureData) (bool, error) {
	verify, err := n.verifySignature(data)
	if err != nil {
		return false, fmt.Errorf("fail to verify signature: %w", err)
	}
	if !verify {
		return false, nil
	}
	n.resp <- data
	return true, nil
}

// GetResponseChannel the final signature gathered from keysign party will be returned from the channel
func (n *Notifier) GetResponseChannel() <-chan []*bc.SignatureData {
	return n.resp
}
