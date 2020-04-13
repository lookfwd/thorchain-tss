package common

import (
	"errors"
	"gitlab.com/thorchain/tss/go-tss/messages"
	"sync"
	"time"
)

const (
	BlameHashCheck     = "hash check failed"
	BlameTssTimeout    = "Tss timeout"
	BlameTssSync       = "signers fail to sync before keygen/keysign"
	BlameInternalError = "fail to start the join party "
)

var (
	ErrHashFromOwner     = errors.New(" hash sent from data owner")
	ErrMsgHashCheck      = errors.New("message we received does not match the majority")
	ErrHashFromPeer      = errors.New("hashcheck error from peer")
	ErrTssTimeOut        = errors.New("error Tss Timeout")
	ErrHashCheck         = errors.New("error in processing hash check")
	ErrHashInconsistency = errors.New("fail to agree on the hash value")
)

type TssConfig struct {
	// KeyGenTimeoutSeconds defines how long do we wait the keygen parties to pass messages along
	KeyGenTimeout time.Duration
	// KeySignTimeoutSeconds defines how long do we wait keysign
	KeySignTimeout time.Duration
	// Pre-parameter define the pre-parameter generations timeout
	PreParamTimeout time.Duration
}

type TssMsgStored struct {
	storedMsg map[string]*messages.WireMessage
	locker    *sync.Mutex
}

type TssStatus struct {
	// Starttime indicates when the Tss server starts
	Starttime time.Time `json:"start_time"`
	// SucKeyGen indicates how many times we run keygen successfully
	SucKeyGen uint64 `json:"successful_keygen"`
	// FailedKeyGen indicates how many times we run keygen unsuccessfully(the invalid http request is not counted as
	// the failure of keygen)
	FailedKeyGen uint64 `json:"failed_keygen"`
	// SucKeySign indicates how many times we run keySign successfully
	SucKeySign uint64 `json:"successful_keysign"`
	// FailedKeySign indicates how many times we run keysign unsuccessfully(the invalid http request is not counted as
	// the failure of keysign)
	FailedKeySign uint64 `json:"failed_keysign"`
}
