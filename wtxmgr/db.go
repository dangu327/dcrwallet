/*
 * Copyright (c) 2015 The btcsuite developers
 * Copyright (c) 2015 The Decred developers
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package wtxmgr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/btcsuite/golangcrypto/ripemd160"
	"github.com/decred/dcrd/blockchain/stake"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrutil"
	"github.com/decred/dcrwallet/walletdb"
)

// Naming
//
// The following variables are commonly used in this file and given
// reserved names:
//
//   ns: The namespace bucket for this package
//   b:  The primary bucket being operated on
//   k:  A single bucket key
//   v:  A single bucket value
//   c:  A bucket cursor
//   ck: The current cursor key
//   cv: The current cursor value
//
// Functions use the naming scheme `Op[Raw]Type[Field]`, which performs the
// operation `Op` on the type `Type`, optionally dealing with raw keys and
// values if `Raw` is used.  Fetch and extract operations may only need to read
// some portion of a key or value, in which case `Field` describes the component
// being returned.  The following operations are used:
//
//   key:     return a db key for some data
//   value:   return a db value for some data
//   put:     insert or replace a value into a bucket
//   fetch:   read and return a value
//   read:    read a value into an out parameter
//   exists:  return the raw (nil if not found) value for some data
//   delete:  remove a k/v pair
//   extract: perform an unchecked slice to extract a key or value
//
// Other operations which are specific to the types being operated on
// should be explained in a comment.
//
// TODO Remove all magic numbers and replace them with cursors that are
//      incremented by constants. Comments need to be filled in. Only
//      about 1/2 of functions are properly commented.

const (
	// accountExistsMask is the bitmask for the accountExists bool in
	// the encoded scriptType for credits.
	accountExistsMask = uint8(0x80)

	// Size of various types in bytes.
	boolSize  = 1
	int8Size  = 1
	int16Size = 2
	int32Size = 4
	int64Size = 8
	hashSize  = chainhash.HashSize
)

// scriptType indicates what type of script a pkScript is for the
// purposes of the database. In the future this can allow for very
// fast lookup of the 20-byte (or more) script/public key hash.
// waddrmgr currently takes addresses instead of 20-byte hashes
// for look up, so the script type is unused in favor of using
// txscript to extract the address from a pkScript.
type scriptType uint8

const (
	// scriptTypeNonexisting is the uint8 value representing an
	// unset script type.
	scriptTypeNonexisting = iota

	// scriptTypeUnspecified is the uint8 value representing an
	// unknown or unspecified type of script.
	scriptTypeUnspecified

	// scriptTypeP2PKH is the uint8 value representing a
	// pay-to-public-key-hash script for a regular transaction.
	scriptTypeP2PKH

	// scriptTypeP2PK is the uint8 value representing a
	// pay-to-public-key script for a regular transaction.
	scriptTypeP2PK

	// scriptTypeP2PKHAlt is the uint8 value representing a
	// pay-to-public-key-hash script for a regular transaction
	// with an alternative ECDSA.
	scriptTypeP2PKHAlt

	// scriptTypeP2PKAlt is the uint8 value representing a
	// pay-to-public-key script for a regular transaction with
	// an alternative ECDSA.
	scriptTypeP2PKAlt

	// scriptTypeP2SH is the uint8 value representing a
	// pay-to-script-hash script for a regular transaction.
	scriptTypeP2SH

	// scriptTypeSP2PKH is the uint8 value representing a
	// pay-to-public-key-hash script for a stake transaction.
	scriptTypeSP2PKH

	// scriptTypeP2SH is the uint8 value representing a
	// pay-to-script-hash script for a stake transaction.
	scriptTypeSP2SH
)

const (
	// scriptLocNotStored is the offset value indicating that
	// the output was stored as a legacy credit and that the
	// script location was not stored.
	scriptLocNotStored = 0

	// hashOffsetP2PKH is the offset location of a pubkey
	// hash in an output's pkScript.
	hashOffsetP2PKH    = 3
	hashOffsetP2SH     = 2
	hashOffsetSP2PKH   = 4
	hashOffsetSP2SH    = 3
	hashOffsetP2PKHAlt = 3
)

// Big endian is the preferred byte order, due to cursor scans over integer
// keys iterating in order.
var byteOrder = binary.BigEndian

// Database versions.  Versions start at 1 and increment for each database
// change.
const (
	// LatestVersion is the most recent store version.
	LatestVersion = 2
)

// This package makes assumptions that the width of a chainhash.Hash is always 32
// bytes.  If this is ever changed (unlikely for bitcoin, possible for alts),
// offsets have to be rewritten.  Use a compile-time assertion that this
// assumption holds true.
var _ [32]byte = chainhash.Hash{}

// Bucket names
var (
	bucketBlocks         = []byte("b")
	bucketTxRecords      = []byte("t")
	bucketCredits        = []byte("c")
	bucketUnspent        = []byte("u")
	bucketDebits         = []byte("d")
	bucketUnmined        = []byte("m")
	bucketUnminedCredits = []byte("mc")
	bucketUnminedInputs  = []byte("mi")
	bucketScripts        = []byte("sc")
	bucketMultisig       = []byte("ms")
	bucketMultisigUsp    = []byte("mu")
)

// Root (namespace) bucket keys
var (
	rootCreateDate   = []byte("date")
	rootVersion      = []byte("vers")
	rootMinedBalance = []byte("bal")
)

// The root bucket's mined balance k/v pair records the total balance for all
// unspent credits from mined transactions.  This includes immature outputs, and
// outputs spent by mempool transactions, which must be considered when
// returning the actual balance for a given number of block confirmations.  The
// value is the amount serialized as a uint64.
func fetchMinedBalance(ns walletdb.Bucket) (dcrutil.Amount, error) {
	v := ns.Get(rootMinedBalance)
	if len(v) != 8 {
		str := fmt.Sprintf("mined balance: short read (expected 8 bytes, "+
			"read %v)", len(v))
		return 0, storeError(ErrData, str, nil)
	}
	return dcrutil.Amount(byteOrder.Uint64(v)), nil
}

func putMinedBalance(ns walletdb.Bucket, amt dcrutil.Amount) error {
	v := make([]byte, 8)
	byteOrder.PutUint64(v, uint64(amt))
	err := ns.Put(rootMinedBalance, v)
	if err != nil {
		str := "failed to put balance"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// Several data structures are given canonical serialization formats as either
// keys or values.  These common formats allow keys and values to be reused
// across different buckets.
//
// The canonical outpoint serialization format is:
//
//   [0:32]  Trasaction hash (32 bytes)
//   [32:36] Output index (4 bytes)
//
// The canonical transaction hash serialization is simply the hash.

func canonicalOutPoint(txHash *chainhash.Hash, index uint32) []byte {
	k := make([]byte, 36)
	copy(k, txHash[:])
	byteOrder.PutUint32(k[32:36], index)
	return k
}

func readCanonicalOutPoint(k []byte, op *wire.OutPoint) error {
	if len(k) < 36 {
		str := "short canonical outpoint"
		return storeError(ErrData, str, nil)
	}
	copy(op.Hash[:], k)
	op.Index = byteOrder.Uint32(k[32:36])
	return nil
}

// Details regarding blocks are saved as k/v pairs in the blocks bucket.
// blockRecords are keyed by their height.  The value is serialized as such:
//
//   [0:32]  Hash (32 bytes)
//   [32:40] Unix time (8 bytes)
//   [40:42] VoteBits (2 bytes/uint16)
//   [42:46] Number of transaction hashes (4 bytes)
//   [46:]   For each transaction hash:
//             Hash (32 bytes)

func keyBlockRecord(height int32) []byte {
	k := make([]byte, 4)
	byteOrder.PutUint32(k, uint32(height))
	return k
}

func valueBlockRecordEmpty(block *BlockMeta) []byte {
	v := make([]byte, 46)
	copy(v, block.Hash[:])
	byteOrder.PutUint64(v[32:40], uint64(block.Time.Unix()))
	byteOrder.PutUint16(v[40:42], block.VoteBits)
	byteOrder.PutUint32(v[42:46], 0)
	return v
}

func valueBlockRecord(block *BlockMeta, txHash *chainhash.Hash) []byte {
	v := make([]byte, 78)
	copy(v, block.Hash[:])
	byteOrder.PutUint64(v[32:40], uint64(block.Time.Unix()))
	byteOrder.PutUint16(v[40:42], block.VoteBits)
	byteOrder.PutUint32(v[42:46], 1)
	copy(v[46:78], txHash[:])
	return v
}

// appendRawBlockRecord returns a new block record value with a transaction
// hash appended to the end and an incremented number of transactions.
func appendRawBlockRecord(v []byte, txHash *chainhash.Hash) ([]byte, error) {
	if len(v) < 46 {
		str := fmt.Sprintf("%s: appendRawBlockRecord short read "+
			"(expected %d bytes, read %d)", bucketBlocks, 46, len(v))
		return nil, storeError(ErrData, str, nil)
	}
	newv := append(v[:len(v):len(v)], txHash[:]...)
	n := byteOrder.Uint32(newv[42:46])
	byteOrder.PutUint32(newv[42:46], n+1)
	return newv, nil
}

// removeRawBlockRecord returns a new block record value with a transaction
// hash removed from the list of transaction and a decremented number of
// transactions.
func removeRawBlockRecord(v []byte, txHash *chainhash.Hash) ([]byte, error) {
	length := len(v)
	if length < 46 {
		str := fmt.Sprintf("%s: short read for removeRawBlockRecord "+
			"(expected %d bytes, read %d)", bucketBlocks, 46, len(v))
		return nil, storeError(ErrData, str, nil)
	}

	newLength := length - 32 // size of hash
	oldNumHashes := (length - 46) / 32
	newValue := make([]byte, newLength, newLength)
	copy(newValue[0:46], v[0:46])

	cursor := 46
	newCursor := 46

	// Only copy the hash in the new value if it's not the one we want to
	// remove.
	for i := 0; i < oldNumHashes; i++ {
		h, err := chainhash.NewHash(v[cursor : cursor+32])
		if err != nil {
			return nil, err
		}

		if h.IsEqual(txHash) {
			cursor += 32
			continue
		}

		copy(newValue[newCursor:newCursor+32], v[cursor:cursor+32])
		cursor += 32
		newCursor += 32
	}

	n := byteOrder.Uint32(newValue[42:46])
	byteOrder.PutUint32(newValue[42:46], n-1)
	return newValue, nil
}

func putRawBlockRecord(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketBlocks).Put(k, v)
	if err != nil {
		str := "failed to store block"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putBlockRecord(ns walletdb.Bucket, block *BlockMeta,
	txHash *chainhash.Hash) error {
	k := keyBlockRecord(block.Height)
	v := valueBlockRecord(block, txHash)
	return putRawBlockRecord(ns, k, v)
}

func fetchBlockTime(ns walletdb.Bucket, height int32) (time.Time, error) {
	k := keyBlockRecord(height)
	v := ns.Bucket(bucketBlocks).Get(k)
	if len(v) < 46 {
		str := fmt.Sprintf("%s: short read for fetchBlockTime (expected "+
			"%d bytes, read %d)", bucketBlocks, 46, len(v))
		return time.Time{}, storeError(ErrData, str, nil)
	}
	return time.Unix(int64(byteOrder.Uint64(v[32:40])), 0), nil
}

func fetchBlockRecord(ns walletdb.Bucket, height int32) (*blockRecord, error) {
	br := &blockRecord{}
	k := keyBlockRecord(height)
	v := ns.Bucket(bucketBlocks).Get(k)
	err := readRawBlockRecord(k, v, br)

	return br, err
}

func fetchChainHeight(ns walletdb.Bucket, startHeight int32) (int32, error) {
	lastValidHeight := int32(0)
	for i := startHeight; ; i++ {
		_, v := existsBlockRecord(ns, i)
		if v == nil {
			break
		}

		lastValidHeight = i
	}

	if lastValidHeight == 0 {
		return 0, fmt.Errorf("blockchain could not iterate to top block")
	}

	return lastValidHeight, nil
}

func existsBlockRecord(ns walletdb.Bucket, height int32) (k, v []byte) {
	k = keyBlockRecord(height)
	v = ns.Bucket(bucketBlocks).Get(k)
	return
}

func readRawBlockRecord(k, v []byte, block *blockRecord) error {
	if len(k) < 4 {
		str := fmt.Sprintf("%s: short key for readRawBlockRecord (expected "+
			"%d bytes, read %d)", bucketBlocks, 4, len(k))
		return storeError(ErrData, str, nil)
	}
	if len(v) < 46 {
		str := fmt.Sprintf("%s: short value read for readRawBlockRecord "+
			"(expected %d bytes, read %d)", bucketBlocks, 46, len(v))
		return storeError(ErrData, str, nil)
	}

	numTransactions := int(byteOrder.Uint32(v[42:46]))
	expectedLen := 46 + chainhash.HashSize*numTransactions
	if len(v) < expectedLen {
		str := fmt.Sprintf("%s: short read readRawBlockRecord for hashes "+
			"(expected %d bytes, read %d)", bucketBlocks, expectedLen, len(v))
		return storeError(ErrData, str, nil)
	}

	block.Height = int32(byteOrder.Uint32(k))
	copy(block.Hash[:], v)
	block.Time = time.Unix(int64(byteOrder.Uint64(v[32:40])), 0)
	block.VoteBits = byteOrder.Uint16(v[40:42])
	block.transactions = make([]chainhash.Hash, numTransactions)
	off := 46
	for i := range block.transactions {
		copy(block.transactions[i][:], v[off:])
		off += chainhash.HashSize
	}

	return nil
}

type blockIterator struct {
	c    walletdb.Cursor
	seek []byte
	ck   []byte
	cv   []byte
	elem blockRecord
	err  error
}

func makeBlockIterator(ns walletdb.Bucket, height int32) blockIterator {
	seek := make([]byte, 4)
	byteOrder.PutUint32(seek, uint32(height))
	c := ns.Bucket(bucketBlocks).Cursor()
	return blockIterator{c: c, seek: seek}
}

// Works just like makeBlockIterator but will initially position the cursor at
// the last k/v pair.  Use this with blockIterator.prev.
func makeReverseBlockIterator(ns walletdb.Bucket) blockIterator {
	seek := make([]byte, 4)
	byteOrder.PutUint32(seek, ^uint32(0))
	c := ns.Bucket(bucketBlocks).Cursor()
	return blockIterator{c: c, seek: seek}
}

func (it *blockIterator) next() bool {
	if it.c == nil {
		return false
	}

	if it.ck == nil {
		it.ck, it.cv = it.c.Seek(it.seek)
	} else {
		it.ck, it.cv = it.c.Next()
	}
	if it.ck == nil {
		it.c = nil
		return false
	}

	err := readRawBlockRecord(it.ck, it.cv, &it.elem)
	if err != nil {
		it.c = nil
		it.err = err
		return false
	}

	return true
}

func (it *blockIterator) prev() bool {
	if it.c == nil {
		return false
	}

	if it.ck == nil {
		it.ck, it.cv = it.c.Seek(it.seek)
		// Seek positions the cursor at the next k/v pair if one with
		// this prefix was not found.  If this happened (the prefixes
		// won't match in this case) move the cursor backward.
		//
		// This technically does not correct for multiple keys with
		// matching prefixes by moving the cursor to the last matching
		// key, but this doesn't need to be considered when dealing with
		// block records since the key (and seek prefix) is just the
		// block height.
		if !bytes.HasPrefix(it.ck, it.seek) {
			it.ck, it.cv = it.c.Prev()
		}
	} else {
		it.ck, it.cv = it.c.Prev()
	}
	if it.ck == nil {
		it.c = nil
		return false
	}

	err := readRawBlockRecord(it.ck, it.cv, &it.elem)
	if err != nil {
		it.c = nil
		it.err = err
		return false
	}

	return true
}

func (it *blockIterator) delete() error {
	err := it.c.Delete()
	if err != nil {
		str := "failed to delete block record"
		storeError(ErrDatabase, str, err)
	}
	return nil
}

func deleteBlockRecord(ns walletdb.Bucket, height int32) error {
	k := keyBlockRecord(height)
	return ns.Bucket(bucketBlocks).Delete(k)
}

// Transaction records are keyed as such:
//
//   [0:32]  Transaction hash (32 bytes)
//   [32:36] Block height (4 bytes)
//   [36:68] Block hash (32 bytes)
//
// The leading transaction hash allows to prefix filter for all records with
// a matching hash.  The block height and hash records a particular incidence
// of the transaction in the blockchain.
//
// The record value is serialized as such:
//
//   [0:8]   Received time (8 bytes)
//   [8:]    Serialized transaction (varies)

func keyTxRecord(txHash *chainhash.Hash, block *Block) []byte {
	k := make([]byte, 68)
	copy(k, txHash[:])
	byteOrder.PutUint32(k[32:36], uint32(block.Height))
	copy(k[36:68], block.Hash[:])
	return k
}

func valueTxRecord(rec *TxRecord) ([]byte, error) {
	var v []byte
	if rec.SerializedTx == nil {
		txSize := rec.MsgTx.SerializeSize()
		v = make([]byte, 8, 8+txSize)
		err := rec.MsgTx.Serialize(bytes.NewBuffer(v[8:]))
		if err != nil {
			str := fmt.Sprintf("unable to serialize transaction %v", rec.Hash)
			return nil, storeError(ErrInput, str, err)
		}
		v = v[:cap(v)]
	} else {
		v = make([]byte, 8+len(rec.SerializedTx))
		copy(v[8:], rec.SerializedTx)
	}
	byteOrder.PutUint64(v, uint64(rec.Received.Unix()))
	return v, nil
}

func putTxRecord(ns walletdb.Bucket, rec *TxRecord, block *Block) error {
	k := keyTxRecord(&rec.Hash, block)
	v, err := valueTxRecord(rec)
	if err != nil {
		return err
	}
	err = ns.Bucket(bucketTxRecords).Put(k, v)
	if err != nil {
		str := fmt.Sprintf("%s: put failed for %v", bucketTxRecords, rec.Hash)
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putRawTxRecord(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketTxRecords).Put(k, v)
	if err != nil {
		str := fmt.Sprintf("%s: put failed", bucketTxRecords)
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func readRawTxRecord(txHash *chainhash.Hash, v []byte, rec *TxRecord) error {
	if len(v) < 8 {
		str := fmt.Sprintf("%s: short read for raw tx record (expected %d "+
			"bytes, read %d, txHash %v)", bucketTxRecords, 8, len(v), txHash)
		return storeError(ErrData, str, nil)
	}
	rec.Hash = *txHash
	rec.Received = time.Unix(int64(byteOrder.Uint64(v)), 0)
	err := rec.MsgTx.Deserialize(bytes.NewReader(v[8:]))
	if err != nil {
		str := fmt.Sprintf("%s: failed to deserialize transaction %v",
			bucketTxRecords, txHash)
		return storeError(ErrData, str, err)
	}

	// Calculate the stake TxType from the MsgTx.
	rec.TxType = stake.DetermineTxType(dcrutil.NewTx(&rec.MsgTx))

	return nil
}

func readRawTxRecordBlock(k []byte, block *Block) error {
	if len(k) < 68 {
		str := fmt.Sprintf("%s: short key (expected %d bytes, read %d)",
			bucketTxRecords, 68, len(k))
		return storeError(ErrData, str, nil)
	}
	block.Height = int32(byteOrder.Uint32(k[32:36]))
	copy(block.Hash[:], k[36:68])
	return nil
}

func fetchRawTxRecordPkScript(k, v []byte, index uint32, scrLoc uint32,
	scrLen uint32) ([]byte, error) {
	var pkScript []byte

	// The script isn't stored (legacy credits). Deserialize the
	// entire transaction.
	if scrLoc == scriptLocNotStored {
		var rec TxRecord
		copy(rec.Hash[:], k) // Silly but need an array
		err := readRawTxRecord(&rec.Hash, v, &rec)
		if err != nil {
			return nil, err
		}
		if int(index) >= len(rec.MsgTx.TxOut) {
			str := "missing transaction output for credit index"
			return nil, storeError(ErrData, str, nil)
		}
		pkScript = rec.MsgTx.TxOut[index].PkScript
	} else {
		// We have the location and script length stored. Just
		// copy the script. Offset the script location for the
		// timestamp that prefixes it.
		scrLocInt := int(scrLoc) + int64Size
		scrLenInt := int(scrLen)
		pkScript = make([]byte, scrLenInt)
		copy(pkScript, v[scrLocInt:scrLocInt+scrLenInt])
	}

	return pkScript, nil
}

func fetchRawTxRecordReceived(v []byte) time.Time {
	return time.Unix(int64(byteOrder.Uint64(v)), 0)
}

func fetchTxRecord(ns walletdb.Bucket, txHash *chainhash.Hash, block *Block) (*TxRecord, error) {
	k := keyTxRecord(txHash, block)
	v := ns.Bucket(bucketTxRecords).Get(k)

	rec := new(TxRecord)
	err := readRawTxRecord(txHash, v, rec)
	return rec, err
}

func existsTxRecord(ns walletdb.Bucket, txHash *chainhash.Hash, block *Block) (k, v []byte) {
	k = keyTxRecord(txHash, block)
	v = ns.Bucket(bucketTxRecords).Get(k)
	return
}

func existsRawTxRecord(ns walletdb.Bucket, k []byte) (v []byte) {
	return ns.Bucket(bucketTxRecords).Get(k)
}

func deleteTxRecord(ns walletdb.Bucket, txHash *chainhash.Hash, block *Block) error {
	k := keyTxRecord(txHash, block)
	return ns.Bucket(bucketTxRecords).Delete(k)
}

// latestTxRecord searches for the newest recorded mined transaction record with
// a matching hash.  In case of a hash collision, the record from the newest
// block is returned.  Returns (nil, nil) if no matching transactions are found.
func latestTxRecord(ns walletdb.Bucket, txHash *chainhash.Hash) (k, v []byte) {
	prefix := txHash[:]
	c := ns.Bucket(bucketTxRecords).Cursor()
	ck, cv := c.Seek(prefix)
	var lastKey, lastVal []byte
	for bytes.HasPrefix(ck, prefix) {
		lastKey, lastVal = ck, cv
		ck, cv = c.Next()
	}
	return lastKey, lastVal
}

// All transaction credits (outputs) are keyed as such:
//
//   [0:32]  Transaction hash (32 bytes)
//   [32:36] Block height (4 bytes)
//   [36:68] Block hash (32 bytes)
//   [68:72] Output index (4 bytes)
//
// The first 68 bytes match the key for the transaction record and may be used
// as a prefix filter to iterate through all credits in order.
//
// The credit value is serialized as such:
//
//   [0:8]   Amount (8 bytes)
//   [8]     Flags (1 byte)
//             [0]: Spent
//             [1]: Change
//             [2:5]: P2PKH stake flag
//                 000: None (translates to OP_NOP10)
//                 001: OP_SSTX
//                 010: OP_SSGEN
//                 011: OP_SSRTX
//                 100: OP_SSTXCHANGE
//             [6]: IsCoinbase
//   [9:81]  OPTIONAL Debit bucket key (72 bytes)
//             [9:41]  Spender transaction hash (32 bytes)
//             [41:45] Spender block height (4 bytes)
//             [45:77] Spender block hash (32 bytes)
//             [77:81] Spender transaction input index (4 bytes)
//   [81:86] OPTIONAL scriptPk location in the transaction output (5 bytes)
//             [81] Script type (P2PKH, P2SH, etc) and accountExists
//             [82:86] Byte index (4 bytes, uint32)
//             [86:90] Length of script (4 bytes, uint32)
//             [90:94] Account (4 bytes, uint32)
//
// The optional debits key is only included if the credit is spent by another
// mined debit.

const (
	// creditKeySize is the total size of a credit key in bytes.
	creditKeySize = 72

	// creditValueSize is the total size of a credit value in bytes.
	creditValueSize = 94
)

func keyCredit(txHash *chainhash.Hash, index uint32, block *Block) []byte {
	k := make([]byte, creditKeySize)
	copy(k, txHash[:])
	byteOrder.PutUint32(k[32:36], uint32(block.Height))
	copy(k[36:68], block.Hash[:])
	byteOrder.PutUint32(k[68:72], index)
	return k
}

func condenseOpCode(opCode uint8) byte {
	return (opCode - 0xb9) << 2
}

// valueUnspentCredit creates a new credit value for an unspent credit.  All
// credits are created unspent, and are only marked spent later, so there is no
// value function to create either spent or unspent credits.
func valueUnspentCredit(cred *credit, scrType scriptType, scrLoc uint32,
	scrLen uint32, account uint32) []byte {
	v := make([]byte, creditValueSize)
	byteOrder.PutUint64(v, uint64(cred.amount))
	v[8] = condenseOpCode(cred.opCode)
	if cred.change {
		v[8] |= 1 << 1
	}
	if cred.isCoinbase {
		v[8] |= 1 << 5
	}

	v[81] = byte(scrType)
	v[81] |= accountExistsMask
	byteOrder.PutUint32(v[82:86], scrLoc)
	byteOrder.PutUint32(v[86:90], scrLen)
	byteOrder.PutUint32(v[90:94], account)

	return v
}

func putRawCredit(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketCredits).Put(k, v)
	if err != nil {
		str := "failed to put credit"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// putUnspentCredit puts a credit record for an unspent credit.  It may only be
// used when the credit is already know to be unspent, or spent by an
// unconfirmed transaction.
func putUnspentCredit(ns walletdb.Bucket, cred *credit, scrType scriptType,
	scrLoc uint32, scrLen uint32, account uint32) error {
	k := keyCredit(&cred.outPoint.Hash, cred.outPoint.Index, &cred.block)
	v := valueUnspentCredit(cred, scrType, scrLoc, scrLen, account)
	return putRawCredit(ns, k, v)
}

func extractRawCreditTxHash(k []byte) chainhash.Hash {
	hash, _ := chainhash.NewHash(k[0:32])
	return *hash
}

func extractRawCreditTxRecordKey(k []byte) []byte {
	return k[0:68]
}

func extractRawCreditBlock(k []byte) *Block {
	hashBytes := k[36:68]
	hash, _ := chainhash.NewHash(hashBytes)
	height := int32(byteOrder.Uint32(k[32:36]))
	return &Block{*hash, height}
}

func extractRawCreditHeight(k []byte) int32 {
	return int32(byteOrder.Uint32(k[32:36]))
}

func extractRawCreditIndex(k []byte) uint32 {
	return byteOrder.Uint32(k[68:72])
}

// fetchRawCreditAmount returns the amount of the credit.
func fetchRawCreditAmount(v []byte) (dcrutil.Amount, error) {
	if len(v) < 9 {
		str := fmt.Sprintf("%s: short read for raw credit amount (expected %d "+
			"bytes, read %d)", bucketCredits, 9, len(v))
		return 0, storeError(ErrData, str, nil)
	}
	return dcrutil.Amount(byteOrder.Uint64(v)), nil
}

// fetchRawCreditAmountSpent returns the amount of the credit and whether the
// credit is spent.
func fetchRawCreditAmountSpent(v []byte) (dcrutil.Amount, bool, error) {
	if len(v) < 9 {
		str := fmt.Sprintf("%s: short read for raw credit amount spent "+
			"(expected %d bytes, read %d)", bucketCredits, 9, len(v))
		return 0, false, storeError(ErrData, str, nil)
	}
	return dcrutil.Amount(byteOrder.Uint64(v)), v[8]&(1<<0) != 0, nil
}

// fetchRawCreditAmountChange returns the amount of the credit and whether the
// credit is marked as change.
func fetchRawCreditAmountChange(v []byte) (dcrutil.Amount, bool, error) {
	if len(v) < 9 {
		str := fmt.Sprintf("%s: short read for raw credit amount change "+
			"(expected %d bytes, read %d)", bucketCredits, 9, len(v))
		return 0, false, storeError(ErrData, str, nil)
	}
	return dcrutil.Amount(byteOrder.Uint64(v)), v[8]&(1<<1) != 0, nil
}

// fetchRawCreditUnspentValue returns the unspent value for a raw credit key.
// This may be used to mark a credit as unspent.
func fetchRawCreditUnspentValue(k []byte) ([]byte, error) {
	if len(k) < 72 {
		str := fmt.Sprintf("%s: short key (expected %d bytes, read %d)",
			bucketCredits, 72, len(k))
		return nil, storeError(ErrData, str, nil)
	}
	return k[32:68], nil
}

// fetchRawCreditTagOpCode fetches the compressed OP code for a transaction.
func fetchRawCreditTagOpCode(v []byte) uint8 {
	return (((v[8] >> 2) & 0x07) + 0xb9)
}

// fetchRawCreditIsCoinbase returns whether or not the credit is a coinbase
// output or not.
func fetchRawCreditIsCoinbase(v []byte) bool {
	return v[8]&(1<<5) != 0
}

// fetchRawCreditScriptType returns the scriptType for the pkScript of this
// credit.
func fetchRawCreditScriptType(v []byte) scriptType {
	if len(v) < creditValueSize {
		return scriptTypeNonexisting
	}
	return scriptType(v[81] & ^accountExistsMask)
}

// fetchRawCreditScriptOffset returns the ScriptOffset for the pkScript of this
// credit.
func fetchRawCreditScriptOffset(v []byte) uint32 {
	if len(v) < creditValueSize {
		return 0
	}
	return byteOrder.Uint32(v[82:86])
}

// fetchRawCreditScriptLength returns the ScriptOffset for the pkScript of this
// credit.
func fetchRawCreditScriptLength(v []byte) uint32 {
	if len(v) < creditValueSize {
		return 0
	}
	return byteOrder.Uint32(v[86:90])
}

// fetchRawCreditAccount returns the account for the pkScript of this
// credit.
func fetchRawCreditAccount(v []byte) (uint32, error) {
	if len(v) < creditValueSize {
		str := "short credit value"
		return 0, storeError(ErrData, str, nil)
	}

	// Was the account ever set?
	if v[81]&accountExistsMask != accountExistsMask {
		str := "account value unset"
		return 0, storeError(ErrValueNoExists, str, nil)
	}

	return byteOrder.Uint32(v[90:94]), nil
}

// spendRawCredit marks the credit with a given key as mined at some particular
// block as spent by the input at some transaction incidence.  The debited
// amount is returned.
func spendCredit(ns walletdb.Bucket, k []byte, spender *indexedIncidence) (dcrutil.Amount, error) {
	v := ns.Bucket(bucketCredits).Get(k)
	newv := make([]byte, creditValueSize)
	copy(newv, v)
	v = newv
	v[8] |= 1 << 0
	copy(v[9:41], spender.txHash[:])
	byteOrder.PutUint32(v[41:45], uint32(spender.block.Height))
	copy(v[45:77], spender.block.Hash[:])
	byteOrder.PutUint32(v[77:81], spender.index)

	return dcrutil.Amount(byteOrder.Uint64(v[0:8])), putRawCredit(ns, k, v)
}

// unspendRawCredit rewrites the credit for the given key as unspent.  The
// output amount of the credit is returned.  It returns without error if no
// credit exists for the key.
func unspendRawCredit(ns walletdb.Bucket, k []byte) (dcrutil.Amount, error) {
	b := ns.Bucket(bucketCredits)
	v := b.Get(k)
	if v == nil {
		return 0, nil
	}
	newv := make([]byte, creditValueSize)
	copy(newv, v)
	newv[8] &^= 1 << 0

	err := b.Put(k, newv)
	if err != nil {
		str := "failed to put credit"
		return 0, storeError(ErrDatabase, str, err)
	}
	return dcrutil.Amount(byteOrder.Uint64(v[0:8])), nil
}

func existsCredit(ns walletdb.Bucket, txHash *chainhash.Hash, index uint32, block *Block) (k, v []byte) {
	k = keyCredit(txHash, index, block)
	v = ns.Bucket(bucketCredits).Get(k)
	return
}

func existsRawCredit(ns walletdb.Bucket, k []byte) []byte {
	return ns.Bucket(bucketCredits).Get(k)
}

func deleteRawCredit(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketCredits).Delete(k)
	if err != nil {
		str := "failed to delete credit"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// creditIterator allows for in-order iteration of all credit records for a
// mined transaction.
//
// Example usage:
//
//   prefix := keyTxRecord(txHash, block)
//   it := makeCreditIterator(ns, prefix)
//   for it.next() {
//           // Use it.elem
//           // If necessary, read additional details from it.ck, it.cv
//   }
//   if it.err != nil {
//           // Handle error
//   }
//
// The elem's Spent field is not set to true if the credit is spent by an
// unmined transaction.  To check for this case:
//
//   k := canonicalOutPoint(&txHash, it.elem.Index)
//   it.elem.Spent = existsRawUnminedInput(ns, k) != nil
type creditIterator struct {
	c      walletdb.Cursor // Set to nil after final iteration
	prefix []byte
	ck     []byte
	cv     []byte
	elem   CreditRecord
	err    error
}

func makeCreditIterator(ns walletdb.Bucket, prefix []byte) creditIterator {
	c := ns.Bucket(bucketCredits).Cursor()
	return creditIterator{c: c, prefix: prefix}
}

func (it *creditIterator) readElem() error {
	if len(it.ck) < 72 {
		str := fmt.Sprintf("%s: short key for credit iterator key "+
			"(expected %d bytes, read %d)", bucketCredits, 72, len(it.ck))
		return storeError(ErrData, str, nil)
	}
	if len(it.cv) < 9 {
		str := fmt.Sprintf("%s: short read for credit iterator value "+
			"(expected %d bytes, read %d)", bucketCredits, 9, len(it.cv))
		return storeError(ErrData, str, nil)
	}
	it.elem.Index = byteOrder.Uint32(it.ck[68:72])
	it.elem.Amount = dcrutil.Amount(byteOrder.Uint64(it.cv))
	it.elem.Spent = it.cv[8]&(1<<0) != 0
	it.elem.Change = it.cv[8]&(1<<1) != 0
	it.elem.OpCode = fetchRawCreditTagOpCode(it.cv)
	it.elem.IsCoinbase = fetchRawCreditIsCoinbase(it.cv)

	return nil
}

func (it *creditIterator) next() bool {
	if it.c == nil {
		return false
	}

	if it.ck == nil {
		it.ck, it.cv = it.c.Seek(it.prefix)
	} else {
		it.ck, it.cv = it.c.Next()
	}
	if !bytes.HasPrefix(it.ck, it.prefix) {
		it.c = nil
		return false
	}

	err := it.readElem()
	if err != nil {
		it.err = err
		return false
	}
	return true
}

// The unspent index records all outpoints for mined credits which are not spent
// by any other mined transaction records (but may be spent by a mempool
// transaction).
//
// Keys are use the canonical outpoint serialization:
//
//   [0:32]  Transaction hash (32 bytes)
//   [32:36] Output index (4 bytes)
//
// Values are serialized as such:
//
//   [0:4]   Block height (4 bytes)
//   [4:36]  Block hash (32 bytes)

func valueUnspent(block *Block) []byte {
	v := make([]byte, 36)
	byteOrder.PutUint32(v, uint32(block.Height))
	copy(v[4:36], block.Hash[:])
	return v
}

func putUnspent(ns walletdb.Bucket, outPoint *wire.OutPoint, block *Block) error {
	k := canonicalOutPoint(&outPoint.Hash, outPoint.Index)
	v := valueUnspent(block)
	err := ns.Bucket(bucketUnspent).Put(k, v)
	if err != nil {
		str := "cannot put unspent"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putRawUnspent(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketUnspent).Put(k, v)
	if err != nil {
		str := "cannot put unspent"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func readUnspentBlock(v []byte, block *Block) error {
	if len(v) < 36 {
		str := "short unspent value"
		return storeError(ErrData, str, nil)
	}
	block.Height = int32(byteOrder.Uint32(v))
	copy(block.Hash[:], v[4:36])
	return nil
}

// existsUnspent returns the key for the unspent output and the corresponding
// key for the credits bucket.  If there is no unspent output recorded, the
// credit key is nil.
func existsUnspent(ns walletdb.Bucket, outPoint *wire.OutPoint) (k, credKey []byte) {
	k = canonicalOutPoint(&outPoint.Hash, outPoint.Index)
	credKey = existsRawUnspent(ns, k)
	return k, credKey
}

// existsRawUnspent returns the credit key if there exists an output recorded
// for the raw unspent key.  It returns nil if the k/v pair does not exist.
func existsRawUnspent(ns walletdb.Bucket, k []byte) (credKey []byte) {
	if len(k) < 36 {
		return nil
	}
	v := ns.Bucket(bucketUnspent).Get(k)
	if len(v) < 36 {
		return nil
	}
	credKey = make([]byte, 72)
	copy(credKey, k[:32])
	copy(credKey[32:68], v)
	copy(credKey[68:72], k[32:36])
	return credKey
}

func deleteRawUnspent(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketUnspent).Delete(k)
	if err != nil {
		str := "failed to delete unspent"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// All transaction debits (inputs which spend credits) are keyed as such:
//
//   [0:32]  Transaction hash (32 bytes)
//   [32:36] Block height (4 bytes)
//   [36:68] Block hash (32 bytes)
//   [68:72] Input index (4 bytes)
//
// The first 68 bytes match the key for the transaction record and may be used
// as a prefix filter to iterate through all debits in order.
//
// The debit value is serialized as such:
//
//   [0:8]   Amount (8 bytes)
//   [8:80]  Credits bucket key (72 bytes)
//             [8:40]  Transaction hash (32 bytes)
//             [40:44] Block height (4 bytes)
//             [44:76] Block hash (32 bytes)
//             [76:80] Output index (4 bytes)

func keyDebit(txHash *chainhash.Hash, index uint32, block *Block) []byte {
	k := make([]byte, 72)
	copy(k, txHash[:])
	byteOrder.PutUint32(k[32:36], uint32(block.Height))
	copy(k[36:68], block.Hash[:])
	byteOrder.PutUint32(k[68:72], index)
	return k
}

func putDebit(ns walletdb.Bucket, txHash *chainhash.Hash, index uint32, amount dcrutil.Amount, block *Block, credKey []byte) error {
	k := keyDebit(txHash, index, block)

	v := make([]byte, 80)
	byteOrder.PutUint64(v, uint64(amount))
	copy(v[8:80], credKey)

	err := ns.Bucket(bucketDebits).Put(k, v)
	if err != nil {
		str := fmt.Sprintf("failed to update debit %s input %d",
			txHash, index)
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func extractRawDebitCreditKey(v []byte) []byte {
	return v[8:80]
}

// existsDebit checks for the existance of a debit.  If found, the debit and
// previous credit keys are returned.  If the debit does not exist, both keys
// are nil.
func existsDebit(ns walletdb.Bucket, txHash *chainhash.Hash, index uint32,
	block *Block) (k, credKey []byte, err error) {
	k = keyDebit(txHash, index, block)
	v := ns.Bucket(bucketDebits).Get(k)
	if v == nil {
		return nil, nil, nil
	}
	if len(v) < 80 {
		str := fmt.Sprintf("%s: short read for exists debit (expected 80 "+
			"bytes, read %v)", bucketDebits, len(v))
		return nil, nil, storeError(ErrData, str, nil)
	}
	return k, v[8:80], nil
}

func deleteRawDebit(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketDebits).Delete(k)
	if err != nil {
		str := "failed to delete debit"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// debitIterator allows for in-order iteration of all debit records for a
// mined transaction.
//
// Example usage:
//
//   prefix := keyTxRecord(txHash, block)
//   it := makeDebitIterator(ns, prefix)
//   for it.next() {
//           // Use it.elem
//           // If necessary, read additional details from it.ck, it.cv
//   }
//   if it.err != nil {
//           // Handle error
//   }
type debitIterator struct {
	c      walletdb.Cursor // Set to nil after final iteration
	prefix []byte
	ck     []byte
	cv     []byte
	elem   DebitRecord
	err    error
}

func makeDebitIterator(ns walletdb.Bucket, prefix []byte) debitIterator {
	c := ns.Bucket(bucketDebits).Cursor()
	return debitIterator{c: c, prefix: prefix}
}

func (it *debitIterator) readElem() error {
	if len(it.ck) < 72 {
		str := fmt.Sprintf("%s: short key for debit iterator key "+
			"(expected %d bytes, read %d)", bucketDebits, 72, len(it.ck))
		return storeError(ErrData, str, nil)
	}
	if len(it.cv) < 80 {
		str := fmt.Sprintf("%s: short read for debite iterator value "+
			"(expected %d bytes, read %d)", bucketDebits, 80, len(it.cv))
		return storeError(ErrData, str, nil)
	}
	it.elem.Index = byteOrder.Uint32(it.ck[68:72])
	it.elem.Amount = dcrutil.Amount(byteOrder.Uint64(it.cv))
	return nil
}

func (it *debitIterator) next() bool {
	if it.c == nil {
		return false
	}

	if it.ck == nil {
		it.ck, it.cv = it.c.Seek(it.prefix)
	} else {
		it.ck, it.cv = it.c.Next()
	}
	if !bytes.HasPrefix(it.ck, it.prefix) {
		it.c = nil
		return false
	}

	err := it.readElem()
	if err != nil {
		it.err = err
		return false
	}
	return true
}

// All unmined transactions are saved in the unmined bucket keyed by the
// transaction hash.  The value matches that of mined transaction records:
//
//   [0:8]   Received time (8 bytes)
//   [8:]    Serialized transaction (varies)

func putRawUnmined(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketUnmined).Put(k, v)
	if err != nil {
		str := "failed to put unmined record"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func readRawUnminedHash(k []byte, txHash *chainhash.Hash) error {
	if len(k) < 32 {
		str := "short unmined key"
		return storeError(ErrData, str, nil)
	}
	copy(txHash[:], k)
	return nil
}

func existsRawUnmined(ns walletdb.Bucket, k []byte) (v []byte) {
	return ns.Bucket(bucketUnmined).Get(k)
}

func deleteRawUnmined(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketUnmined).Delete(k)
	if err != nil {
		str := "failed to delete unmined record"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// Unmined transaction credits use the canonical serialization format:
//
//  [0:32]   Transaction hash (32 bytes)
//  [32:36]  Output index (4 bytes)
//
// The value matches the format used by mined credits, but the spent flag is
// never set and the optional debit record is never included.  The simplified
// format is thus:
//
//   [0:8]   Amount (8 bytes)
//   [8]     Flags (1 byte)
//             [1]: Change
//             [2:5]: P2PKH stake flag
//                 000: None (translates to OP_NOP10)
//                 001: OP_SSTX
//                 010: OP_SSGEN
//                 011: OP_SSRTX
//                 100: OP_SSTXCHANGE
//             [6]: Is coinbase
//   [9] Script type (P2PKH, P2SH, etc) and bit flag for account stored
//   [10:14] Byte index (4 bytes, uint32)
//   [14:18] Length of script (4 bytes, uint32)
//   [18:22] Account (4 bytes, uint32)
//
const (
	// unconfCreditKeySize is the total size of an unconfirmed credit
	// key in bytes.
	unconfCreditKeySize = 36

	// unconfValueSizeLegacy is the total size of an unconfirmed legacy
	// credit value in bytes (version 1).
	unconfValueSizeLegacy = 9

	// unconfValueSize is the total size of an unconfirmed credit
	// value in bytes (version 2).
	unconfValueSize = 22
)

func valueUnminedCredit(amount dcrutil.Amount, change bool, opCode uint8,
	IsCoinbase bool, scrType scriptType, scrLoc uint32, scrLen uint32,
	account uint32) []byte {
	v := make([]byte, unconfValueSize)
	byteOrder.PutUint64(v, uint64(amount))
	v[8] = condenseOpCode(opCode)
	if change {
		v[8] |= 1 << 1
	}
	if IsCoinbase {
		v[8] |= 1 << 5
	}

	v[9] = byte(scrType)
	v[9] |= accountExistsMask
	byteOrder.PutUint32(v[10:14], scrLoc)
	byteOrder.PutUint32(v[14:18], scrLen)
	byteOrder.PutUint32(v[18:22], account)

	return v
}

func putRawUnminedCredit(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketUnminedCredits).Put(k, v)
	if err != nil {
		str := "cannot put unmined credit"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func fetchRawUnminedCreditIndex(k []byte) (uint32, error) {
	if len(k) < unconfCreditKeySize {
		str := "short unmined credit key when look up credit idx"
		return 0, storeError(ErrData, str, nil)
	}
	return byteOrder.Uint32(k[32:36]), nil
}

func fetchRawUnminedCreditAmount(v []byte) (dcrutil.Amount, error) {
	if len(v) < unconfValueSizeLegacy {
		str := "short unmined credit value when look up credit amt"
		return 0, storeError(ErrData, str, nil)
	}
	return dcrutil.Amount(byteOrder.Uint64(v)), nil
}

func fetchRawUnminedCreditAmountChange(v []byte) (dcrutil.Amount, bool, error) {
	if len(v) < unconfValueSizeLegacy {
		str := "short unmined credit value when look up credit amt change"
		return 0, false, storeError(ErrData, str, nil)
	}
	amt := dcrutil.Amount(byteOrder.Uint64(v))
	change := v[8]&(1<<1) != 0
	return amt, change, nil
}

func fetchRawUnminedCreditTagOpcode(v []byte) uint8 {
	return (((v[8] >> 2) & 0x07) + 0xb9)
}

func fetchRawUnminedCreditTagIsCoinbase(v []byte) bool {
	return v[8]&(1<<5) != 0
}

func fetchRawUnminedCreditScriptType(v []byte) scriptType {
	if len(v) < unconfValueSize {
		return scriptTypeNonexisting
	}
	return scriptType(v[9] & ^accountExistsMask)
}

func fetchRawUnminedCreditScriptOffset(v []byte) uint32 {
	if len(v) < unconfValueSize {
		return 0
	}
	return byteOrder.Uint32(v[10:14])
}

func fetchRawUnminedCreditScriptLength(v []byte) uint32 {
	if len(v) < unconfValueSize {
		return 0
	}
	return byteOrder.Uint32(v[14:18])
}

func fetchRawUnminedCreditAccount(v []byte) (uint32, error) {
	if len(v) < unconfValueSize {
		str := "short unmined credit value when look up account"
		return 0, storeError(ErrData, str, nil)
	}

	// Was the account ever set?
	if v[9]&accountExistsMask != accountExistsMask {
		str := "account value unset"
		return 0, storeError(ErrValueNoExists, str, nil)
	}

	return byteOrder.Uint32(v[18:22]), nil
}

func existsRawUnminedCredit(ns walletdb.Bucket, k []byte) []byte {
	return ns.Bucket(bucketUnminedCredits).Get(k)
}

func deleteRawUnminedCredit(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketUnminedCredits).Delete(k)
	if err != nil {
		str := "failed to delete unmined credit"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// unminedCreditIterator allows for cursor iteration over all credits, in order,
// from a single unmined transaction.
//
//  Example usage:
//
//   it := makeUnminedCreditIterator(ns, txHash)
//   for it.next() {
//           // Use it.elem, it.ck and it.cv
//           // Optionally, use it.delete() to remove this k/v pair
//   }
//   if it.err != nil {
//           // Handle error
//   }
//
// The spentness of the credit is not looked up for performance reasons (because
// for unspent credits, it requires another lookup in another bucket).  If this
// is needed, it may be checked like this:
//
//   spent := existsRawUnminedInput(ns, it.ck) != nil
type unminedCreditIterator struct {
	c      walletdb.Cursor
	prefix []byte
	ck     []byte
	cv     []byte
	elem   CreditRecord
	err    error
}

func makeUnminedCreditIterator(ns walletdb.Bucket, txHash *chainhash.Hash) unminedCreditIterator {
	c := ns.Bucket(bucketUnminedCredits).Cursor()
	return unminedCreditIterator{c: c, prefix: txHash[:]}
}

func (it *unminedCreditIterator) readElem() error {
	index, err := fetchRawUnminedCreditIndex(it.ck)
	if err != nil {
		return err
	}
	amount, change, err := fetchRawUnminedCreditAmountChange(it.cv)
	if err != nil {
		return err
	}

	it.elem.Index = index
	it.elem.Amount = amount
	it.elem.Change = change
	// Spent intentionally not set

	return nil
}

func (it *unminedCreditIterator) next() bool {
	if it.c == nil {
		return false
	}

	if it.ck == nil {
		it.ck, it.cv = it.c.Seek(it.prefix)
	} else {
		it.ck, it.cv = it.c.Next()
	}
	if !bytes.HasPrefix(it.ck, it.prefix) {
		it.c = nil
		return false
	}

	err := it.readElem()
	if err != nil {
		it.err = err
		return false
	}
	return true
}

func (it *unminedCreditIterator) delete() error {
	err := it.c.Delete()
	if err != nil {
		str := "failed to delete unmined credit"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// OutPoints spent by unmined transactions are saved in the unmined inputs
// bucket.  This bucket maps between each previous output spent, for both mined
// and unmined transactions, to the hash of the unmined transaction.
//
// The key is serialized as such:
//
//   [0:32]   Transaction hash (32 bytes)
//   [32:36]  Output index (4 bytes)
//
// The value is serialized as such:
//
//   [0:32]   Transaction hash (32 bytes)

func putRawUnminedInput(ns walletdb.Bucket, k, v []byte) error {
	err := ns.Bucket(bucketUnminedInputs).Put(k, v)
	if err != nil {
		str := "failed to put unmined input"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func existsRawUnminedInput(ns walletdb.Bucket, k []byte) (v []byte) {
	return ns.Bucket(bucketUnminedInputs).Get(k)
}

func deleteRawUnminedInput(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketUnminedInputs).Delete(k)
	if err != nil {
		str := "failed to delete unmined input"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

// Tx scripts are stored as the raw serialized script. The key in the database
// for the TxScript itself is the hash160 of the script.
func keyTxScript(script []byte) []byte {
	return dcrutil.Hash160(script)
}

func deleteRawTxScript(ns walletdb.Bucket, hash []byte) error {
	err := ns.Bucket(bucketScripts).Delete(hash)
	if err != nil {
		str := "failed to delete tx script"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putTxScript(ns walletdb.Bucket, script []byte) error {
	k := keyTxScript(script)
	err := ns.Bucket(bucketScripts).Put(k, script)
	if err != nil {
		str := "failed to put tx script"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func existsTxScript(ns walletdb.Bucket, hash []byte) []byte {
	vOrig := ns.Bucket(bucketScripts).Get(hash)
	if vOrig == nil {
		return nil
	}
	v := make([]byte, len(vOrig), len(vOrig))
	copy(v, vOrig)
	return v
}

// The multisig bucket stores utxos that are P2SH output scripts to the user.
// These are handled separately and less efficiently than the more typical
// P2PKH types.
// Transactions with multisig outputs are keyed to serialized outpoints:
// [0:32]    Hash (32 bytes)
// [32:36]   Index (uint32)
//
// The value is the following:
// [0:20]    P2SH Hash (20 bytes)
// [20]      m (in m-of-n) (uint8)
// [21]      n (in m-of-n) (uint8)
// [22]      Flags (1 byte)
//           [0]: Spent
//           [1]: Tree
// [23:55]   Block hash (32 byte hash)
// [55:59]   Block height (uint32)
// [59:67]   Amount (int64)
// [67:99]   SpentBy (32 byte hash)
// [99:103]  SpentByIndex (uint32)
// [103:135] TxHash (32 byte hash)
//
// The structure is set up so that the user may easily spend from any unspent
// P2SH multisig outpoints they own an address in.
func keyMultisigOut(hash chainhash.Hash, index uint32) []byte {
	return canonicalOutPoint(&hash, index)
}

func valueMultisigOut(sh [ripemd160.Size]byte, m uint8, n uint8,
	spent bool, tree int8, blockHash chainhash.Hash,
	blockHeight uint32, amount dcrutil.Amount, spentBy chainhash.Hash,
	sbi uint32, txHash chainhash.Hash) []byte {
	v := make([]byte, 135, 135)

	copy(v[0:20], sh[0:20])
	v[20] = m
	v[21] = n
	v[22] = uint8(0)

	if spent {
		v[22] |= 1 << 0
	}

	if tree == dcrutil.TxTreeStake {
		v[22] |= 1 << 1
	}

	copy(v[23:55], blockHash[:])
	byteOrder.PutUint32(v[55:59], blockHeight)
	byteOrder.PutUint64(v[59:67], uint64(amount))

	copy(v[67:99], spentBy[:])
	byteOrder.PutUint32(v[99:103], sbi)

	copy(v[103:135], txHash[:])

	return v
}

func fetchMultisigOut(k, v []byte) (*MultisigOut, error) {
	if len(k) != 36 {
		str := "multisig out k is wrong size"
		return nil, storeError(ErrDatabase, str, nil)
	}
	if len(v) != 135 {
		str := "multisig out v is wrong size"
		return nil, storeError(ErrDatabase, str, nil)
	}

	var mso MultisigOut

	var op wire.OutPoint
	err := readCanonicalOutPoint(k, &op)
	if err != nil {
		return nil, err
	}
	mso.OutPoint = &op
	mso.OutPoint.Tree = dcrutil.TxTreeRegular

	copy(mso.ScriptHash[0:20], v[0:20])

	mso.M = uint8(v[20])
	mso.N = uint8(v[21])
	mso.Spent = v[22]&(1<<0) != 0
	mso.Tree = 0
	isStakeTree := v[22]&(1<<1) != 0
	if isStakeTree {
		mso.Tree = 1
	}

	copy(mso.BlockHash[0:32], v[23:55])
	mso.BlockHeight = byteOrder.Uint32(v[55:59])
	mso.Amount = dcrutil.Amount(byteOrder.Uint64(v[59:67]))

	copy(mso.SpentBy[0:32], v[67:99])
	mso.SpentByIndex = byteOrder.Uint32(v[99:103])

	copy(mso.TxHash[0:32], v[103:135])

	return &mso, nil
}

func fetchMultisigOutScrHash(v []byte) [ripemd160.Size]byte {
	var sh [ripemd160.Size]byte
	copy(sh[0:20], v[0:20])
	return sh
}

func fetchMultisigOutMN(v []byte) (uint8, uint8) {
	return uint8(v[20]), uint8(v[21])
}

func fetchMultisigOutSpent(v []byte) bool {
	spent := v[22]&(1<<0) != 0

	return spent
}

func fetchMultisigOutTree(v []byte) int8 {
	isStakeTree := v[22]&(1<<1) != 0
	tree := dcrutil.TxTreeRegular
	if isStakeTree {
		tree = dcrutil.TxTreeStake
	}

	return tree
}

func fetchMultisigOutSpentVerbose(v []byte) (bool, chainhash.Hash, uint32) {
	spent := v[22]&(1<<0) != 0
	spentBy := chainhash.Hash{}
	copy(spentBy[0:32], v[67:99])
	spentIndex := byteOrder.Uint32(v[99:103])

	return spent, spentBy, spentIndex
}

func fetchMultisigOutMined(v []byte) (chainhash.Hash, uint32) {
	blockHash := chainhash.Hash{}
	copy(blockHash[0:32], v[23:55])
	blockHeight := byteOrder.Uint32(v[55:59])

	return blockHash, blockHeight
}

func fetchMultisigOutAmount(v []byte) dcrutil.Amount {
	return dcrutil.Amount(byteOrder.Uint64(v[59:67]))
}

func setMultisigOutSpent(v []byte, spendHash chainhash.Hash, spendIndex uint32) {
	spentByte := uint8(0)
	spentByte |= 1 << 0
	v[22] = spentByte
	copy(v[67:99], spendHash[:])
	byteOrder.PutUint32(v[99:103], spendIndex)
}

func setMultisigOutUnSpent(v []byte) {
	empty := chainhash.Hash{}
	spentByte := uint8(0)
	v[22] = spentByte
	copy(v[67:98], empty[:])
	byteOrder.PutUint32(v[99:103], 0xFFFFFFFF)
}

func setMultisigOutMined(v []byte, blockHash chainhash.Hash,
	blockHeight uint32) {
	copy(v[23:55], blockHash[:])
	byteOrder.PutUint32(v[55:59], blockHeight)
}

func setMultisigOutUnmined(v []byte) {
	empty := chainhash.Hash{}
	copy(v[23:55], empty[:])
	byteOrder.PutUint32(v[55:59], 0)
}

func deleteMultisigOut(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketMultisig).Delete(k)
	if err != nil {
		str := "failed to delete multisig output"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putMultisigOut(ns walletdb.Bucket, mso *MultisigOut) error {
	msok := keyMultisigOut(mso.OutPoint.Hash,
		mso.OutPoint.Index)
	msov := valueMultisigOut(mso.ScriptHash,
		mso.M,
		mso.N,
		mso.Spent,
		mso.Tree,
		mso.BlockHash,
		mso.BlockHeight,
		mso.Amount,
		mso.SpentBy,
		mso.SpentByIndex,
		mso.TxHash)
	err := ns.Bucket(bucketMultisig).Put(msok, msov)
	if err != nil {
		str := "failed to put multisig output"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putMultisigOutRawValues(ns walletdb.Bucket, k []byte, v []byte) error {
	err := ns.Bucket(bucketMultisig).Put(k, v)
	if err != nil {
		str := "failed to put multisig output"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func existsMultisigOut(ns walletdb.Bucket, k []byte) []byte {
	vOrig := ns.Bucket(bucketMultisig).Get(k)
	if vOrig == nil {
		return nil
	}
	v := make([]byte, 135, 135)
	copy(v, vOrig)
	return v
}

// The multisignature unspent bucket simply keeps a list of all unspent
// multisignature script outpoints. They are keyed [outpoint] -> [blank byte].
func keyMultisigOutUS(hash chainhash.Hash, index uint32) []byte {
	return canonicalOutPoint(&hash, index)
}

func putMultisigOutUS(ns walletdb.Bucket, k []byte) error {
	blank := []byte{0x00}
	err := ns.Bucket(bucketMultisigUsp).Put(k, blank)
	if err != nil {
		str := "failed to put unspent multisig output"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func putMultisigOutOutpointUS(ns walletdb.Bucket, op *wire.OutPoint) error {
	msok := keyMultisigOut(op.Hash,
		op.Index)
	blank := []byte{0x00}
	err := ns.Bucket(bucketMultisigUsp).Put(msok, blank)
	if err != nil {
		str := "failed to put unspent multisig output"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func deleteMultisigOutUS(ns walletdb.Bucket, k []byte) error {
	err := ns.Bucket(bucketMultisigUsp).Delete(k)
	if err != nil {
		str := "failed to delete multisig output"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func existsMultisigOutUS(ns walletdb.Bucket, k []byte) bool {
	v := ns.Bucket(bucketMultisigUsp).Get(k)
	return v != nil
}

// openStore opens an existing transaction store from the passed namespace.  If
// necessary, an already existing store is upgraded to newer db format.
func openStore(namespace walletdb.Namespace) error {
	var version uint32
	err := scopedView(namespace, func(ns walletdb.Bucket) error {
		// Verify a store already exists and upgrade as necessary.
		v := ns.Get(rootVersion)
		if len(v) != 4 {
			return nil
		}
		version = byteOrder.Uint32(v)
		return nil
	})
	if err != nil {
		const desc = "failed to open existing store"
		if serr, ok := err.(Error); ok {
			serr.Desc = desc + ": " + serr.Desc
			return serr
		}
		return storeError(ErrDatabase, desc, err)
	}

	// The initial version is one.  If no store exists and no version was
	// saved, this variable will be zero.
	if version == 0 {
		str := "no transaction store exists in namespace"
		return storeError(ErrNoExists, str, nil)
	}

	// Cannot continue if the saved database is too new for this software.
	// This probably indicates an outdated binary.
	if version > LatestVersion {
		str := fmt.Sprintf("recorded version %d is newer that latest "+
			"understood version %d", version, LatestVersion)
		return storeError(ErrUnknownVersion, str, nil)
	}

	return nil
}

// createStore creates the tx store (with the latest db version) in the passed
// namespace.  If a store already exists, ErrAlreadyExists is returned.
func createStore(namespace walletdb.Namespace) error {
	// Initialize the buckets and root bucket fields as needed.
	err := scopedUpdate(namespace, func(ns walletdb.Bucket) error {
		// Ensure that nothing currently exists in the namespace bucket.
		ck, cv := ns.Cursor().First()
		if ck != nil || cv != nil {
			const str = "namespace is not empty"
			return storeError(ErrAlreadyExists, str, nil)
		}

		// Write the latest store version.
		v := make([]byte, 4)
		byteOrder.PutUint32(v, LatestVersion)
		err := ns.Put(rootVersion, v)
		if err != nil {
			str := "failed to store latest database version"
			return storeError(ErrDatabase, str, err)
		}

		// Save the creation date of the store.
		v = make([]byte, 8)
		byteOrder.PutUint64(v, uint64(time.Now().Unix()))
		err = ns.Put(rootCreateDate, v)
		if err != nil {
			str := "failed to store database creation time"
			return storeError(ErrDatabase, str, err)
		}

		// Write a zero balance.
		v = make([]byte, 8)
		err = ns.Put(rootMinedBalance, v)
		if err != nil {
			str := "failed to write zero balance"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketBlocks)
		if err != nil {
			str := "failed to create blocks bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketTxRecords)
		if err != nil {
			str := "failed to create tx records bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketCredits)
		if err != nil {
			str := "failed to create credits bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketDebits)
		if err != nil {
			str := "failed to create debits bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketUnspent)
		if err != nil {
			str := "failed to create unspent bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketUnmined)
		if err != nil {
			str := "failed to create unmined bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketUnminedCredits)
		if err != nil {
			str := "failed to create unmined credits bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketUnminedInputs)
		if err != nil {
			str := "failed to create unmined inputs bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketScripts)
		if err != nil {
			str := "failed to create scripts bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketMultisig)
		if err != nil {
			str := "failed to create multisig tx bucket"
			return storeError(ErrDatabase, str, err)
		}

		_, err = ns.CreateBucket(bucketMultisigUsp)
		if err != nil {
			str := "failed to create multisig unspent tx bucket"
			return storeError(ErrDatabase, str, err)
		}

		return nil
	})
	if err != nil {
		const desc = "failed to create new store"
		if serr, ok := err.(Error); ok {
			serr.Desc = desc + ": " + serr.Desc
			return serr
		}
		return storeError(ErrDatabase, desc, err)
	}

	return nil
}

func scopedUpdate(ns walletdb.Namespace, f func(walletdb.Bucket) error) error {
	tx, err := ns.Begin(true)
	if err != nil {
		str := "cannot begin update"
		return storeError(ErrDatabase, str, err)
	}
	err = f(tx.RootBucket())
	if err != nil {
		rbErr := tx.Rollback()
		if rbErr != nil {
			const desc = "rollback failed"
			serr, ok := err.(Error)
			if !ok {
				// This really shouldn't happen.
				return storeError(ErrDatabase, desc, rbErr)
			}
			serr.Desc = desc + ": " + serr.Desc
			return serr
		}
		return err
	}
	err = tx.Commit()
	if err != nil {
		str := "commit failed"
		return storeError(ErrDatabase, str, err)
	}
	return nil
}

func scopedView(ns walletdb.Namespace, f func(walletdb.Bucket) error) error {
	tx, err := ns.Begin(false)
	if err != nil {
		str := "cannot begin view"
		return storeError(ErrDatabase, str, err)
	}
	err = f(tx.RootBucket())
	rbErr := tx.Rollback()
	if err != nil {
		return err
	}
	if rbErr != nil {
		str := "cannot close view"
		return storeError(ErrDatabase, str, rbErr)
	}
	return nil
}
