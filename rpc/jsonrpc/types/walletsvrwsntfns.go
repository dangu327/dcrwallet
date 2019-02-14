// Copyright (c) 2014 The btcsuite developers
// Copyright (c) 2015-2019 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// NOTE: This file is intended to house the RPC websocket notifications that are
// supported by a wallet server.

package types

import "github.com/decred/dcrd/dcrjson/v2"

const (
	// AccountBalanceNtfnMethod is the method used for account balance
	// notifications.
	AccountBalanceNtfnMethod = "accountbalance"

	// DcrdConnectedNtfnMethod is the method used for notifications when
	// a wallet server is connected to a chain server.
	DcrdConnectedNtfnMethod = "dcrdconnected"

	// NewTicketsNtfnMethod is the method of the daemon
	// newtickets notification.
	NewTicketsNtfnMethod = "newtickets"

	// NewTxNtfnMethod is the method used to notify that a wallet server has
	// added a new transaction to the transaction store.
	NewTxNtfnMethod = "newtx"

	// RevocationCreatedNtfnMethod is the method of the dcrwallet
	// revocationcreated notification.
	RevocationCreatedNtfnMethod = "revocationcreated"

	// TicketPurchasedNtfnMethod is the method of the dcrwallet
	// ticketpurchased notification.
	TicketPurchasedNtfnMethod = "ticketpurchased"

	// VoteCreatedNtfnMethod is the method of the dcrwallet
	// votecreated notification.
	VoteCreatedNtfnMethod = "votecreated"

	// WinningTicketsNtfnMethod is the method of the daemon
	// winningtickets notification.
	WinningTicketsNtfnMethod = "winningtickets"

	// WalletLockStateNtfnMethod is the method used to notify the lock state
	// of a wallet has changed.
	WalletLockStateNtfnMethod = "walletlockstate"
)

// AccountBalanceNtfn defines the accountbalance JSON-RPC notification.
type AccountBalanceNtfn struct {
	Account   string
	Balance   float64 // In DCR
	Confirmed bool    // Whether Balance is confirmed or unconfirmed.
}

// NewAccountBalanceNtfn returns a new instance which can be used to issue an
// accountbalance JSON-RPC notification.
func NewAccountBalanceNtfn(account string, balance float64, confirmed bool) *AccountBalanceNtfn {
	return &AccountBalanceNtfn{
		Account:   account,
		Balance:   balance,
		Confirmed: confirmed,
	}
}

// DcrdConnectedNtfn defines the dcrddconnected JSON-RPC notification.
type DcrdConnectedNtfn struct {
	Connected bool
}

// NewDcrdConnectedNtfn returns a new instance which can be used to issue a
// dcrddconnected JSON-RPC notification.
func NewDcrdConnectedNtfn(connected bool) *DcrdConnectedNtfn {
	return &DcrdConnectedNtfn{
		Connected: connected,
	}
}

// NewTxNtfn defines the newtx JSON-RPC notification.
type NewTxNtfn struct {
	Account string
	Details ListTransactionsResult
}

// NewNewTxNtfn returns a new instance which can be used to issue a newtx
// JSON-RPC notification.
func NewNewTxNtfn(account string, details ListTransactionsResult) *NewTxNtfn {
	return &NewTxNtfn{
		Account: account,
		Details: details,
	}
}

// TicketPurchasedNtfn is a type handling custom marshaling and
// unmarshaling of ticketpurchased JSON websocket notifications.
type TicketPurchasedNtfn struct {
	TxHash string
	Amount int64 // SStx only
}

// NewTicketPurchasedNtfn creates a new TicketPurchasedNtfn.
func NewTicketPurchasedNtfn(txHash string, amount int64) *TicketPurchasedNtfn {
	return &TicketPurchasedNtfn{
		TxHash: txHash,
		Amount: amount,
	}
}

// RevocationCreatedNtfn is a type handling custom marshaling and
// unmarshaling of ticketpurchased JSON websocket notifications.
type RevocationCreatedNtfn struct {
	TxHash string
	SStxIn string
}

// NewRevocationCreatedNtfn creates a new RevocationCreatedNtfn.
func NewRevocationCreatedNtfn(txHash string, sstxIn string) *RevocationCreatedNtfn {
	return &RevocationCreatedNtfn{
		TxHash: txHash,
		SStxIn: sstxIn,
	}
}

// VoteCreatedNtfn is a type handling custom marshaling and
// unmarshaling of ticketpurchased JSON websocket notifications.
type VoteCreatedNtfn struct {
	TxHash    string
	BlockHash string
	Height    int32
	SStxIn    string
	VoteBits  uint16
}

// NewVoteCreatedNtfn creates a new VoteCreatedNtfn.
func NewVoteCreatedNtfn(txHash string, blockHash string, height int32, sstxIn string, voteBits uint16) *VoteCreatedNtfn {
	return &VoteCreatedNtfn{
		TxHash:    txHash,
		BlockHash: blockHash,
		Height:    height,
		SStxIn:    sstxIn,
		VoteBits:  voteBits,
	}
}

// WalletLockStateNtfn defines the walletlockstate JSON-RPC notification.
type WalletLockStateNtfn struct {
	Locked bool
}

// NewWalletLockStateNtfn returns a new instance which can be used to issue a
// walletlockstate JSON-RPC notification.
func NewWalletLockStateNtfn(locked bool) *WalletLockStateNtfn {
	return &WalletLockStateNtfn{
		Locked: locked,
	}
}

func init() {
	// The commands in this file are only usable with a wallet server via
	// websockets and are notifications.
	flags := dcrjson.UFWalletOnly | dcrjson.UFWebsocketOnly | dcrjson.UFNotification

	dcrjson.MustRegisterCmd(AccountBalanceNtfnMethod, (*AccountBalanceNtfn)(nil), flags)
	dcrjson.MustRegisterCmd(DcrdConnectedNtfnMethod, (*DcrdConnectedNtfn)(nil), flags)
	dcrjson.MustRegisterCmd(NewTxNtfnMethod, (*NewTxNtfn)(nil), flags)
	dcrjson.MustRegisterCmd(TicketPurchasedNtfnMethod, (*TicketPurchasedNtfn)(nil), flags)
	dcrjson.MustRegisterCmd(RevocationCreatedNtfnMethod, (*RevocationCreatedNtfn)(nil), flags)
	dcrjson.MustRegisterCmd(VoteCreatedNtfnMethod, (*VoteCreatedNtfn)(nil), flags)
	dcrjson.MustRegisterCmd(WalletLockStateNtfnMethod, (*WalletLockStateNtfn)(nil), flags)
}
