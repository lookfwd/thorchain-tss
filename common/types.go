package common

import (
	"errors"
	"time"
)

const (
	BlameHashCheck     = "hash check failed"
	BlameTssTimeout    = "Tss timeout"
	BlameTssSync       = "signers fail to sync before keygen/keysign"
	BlameInternalError = "fail to start the join party "
)

var (
	ByPassGeneratePreParam = false
	ErrHashFromOwner       = errors.New("hashcheck error from data owner")
	ErrHashFromPeer        = errors.New("hashcheck error from peer")
	ErrTssTimeOut          = errors.New("error Tss Timeout")
)

type TssConfig struct {
	// KeyGenTimeoutSeconds defines how long do we wait the keygen parties to pass messages along
	KeyGenTimeout time.Duration
	// KeySignTimeoutSeconds defines how long do we wait keysign
	KeySignTimeout time.Duration
	// Pre-parameter define the pre-parameter generations timeout
	PreParamTimeout time.Duration
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
	// CurrKeygen indicates the which keygen round we are in
	CurrKeyGen string `json:"current_keygen"`
	// CurrKeySign indicates the which keysign round we are in
	CurrKeySign string `json:"current_keysign"`
}
