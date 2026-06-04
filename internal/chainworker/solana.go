package chainworker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"

	"filippo.io/edwards25519"
	"github.com/shopspring/decimal"
)

const (
	solanaNativeDecimals        = 9
	solanaCommitmentFinalized   = "finalized"
	solanaPDAConstant           = "ProgramDerivedAddress"
	solanaSystemProgramBase58   = "11111111111111111111111111111111"
	solanaTokenProgramBase58    = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	solanaToken2022Base58       = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
	solanaAssociatedTokenBase58 = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"
)

var (
	solanaSystemProgramID    = solanaConstPubKey(solanaSystemProgramBase58)
	solanaTokenProgramID     = solanaConstPubKey(solanaTokenProgramBase58)
	solanaToken2022ProgramID = solanaConstPubKey(solanaToken2022Base58)
	solanaAssociatedTokenID  = solanaConstPubKey(solanaAssociatedTokenBase58)
)

type solanaPubKey [32]byte

type solanaTokenDefinition struct {
	Mint         string
	Decimals     int
	TokenProgram string
}

type solanaInstruction struct {
	ProgramID solanaPubKey
	Accounts  []solanaInstructionAccount
	Data      []byte
}

type solanaInstructionAccount struct {
	PublicKey solanaPubKey
	Signer    bool
	Writable  bool
}

type solanaCompiledAccount struct {
	PublicKey solanaPubKey
	Signer    bool
	Writable  bool
	Order     int
}

type solanaAccountKey string

func (k *solanaAccountKey) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*k = solanaAccountKey(value)
		return nil
	}
	var obj struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*k = solanaAccountKey(obj.Pubkey)
	return nil
}

type solanaTokenBalance struct {
	AccountIndex int    `json:"accountIndex"`
	Mint         string `json:"mint"`
	Owner        string `json:"owner"`
	TokenAmount  struct {
		Amount   string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}

type solanaTransactionResult struct {
	Slot int64 `json:"slot"`
	Meta struct {
		Err               any                  `json:"err"`
		PreBalances       []json.Number        `json:"preBalances"`
		PostBalances      []json.Number        `json:"postBalances"`
		PreTokenBalances  []solanaTokenBalance `json:"preTokenBalances"`
		PostTokenBalances []solanaTokenBalance `json:"postTokenBalances"`
	} `json:"meta"`
	Transaction struct {
		Message struct {
			AccountKeys []solanaAccountKey `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
}

func (s *Server) solanaStatus(ctx context.Context) (map[string]any, error) {
	slot, err := s.solanaLatestBlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	timestamp, err := s.solanaBlockTime(ctx, slot)
	if err != nil {
		return nil, err
	}
	return map[string]any{"last_block_timestamp": timestamp, "slot": slot}, nil
}

func (s *Server) newSolanaAddress() (address string, privateKey string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base58Encode(public), base58Encode(private), nil
}

func (s *Server) solanaBalance(ctx context.Context, crypto string, address string) (decimal.Decimal, error) {
	owner, err := solanaPubKeyFromBase58(address)
	if err != nil {
		return decimal.Zero, err
	}
	if s.isNative(crypto) {
		var result struct {
			Value json.Number `json:"value"`
		}
		if err := s.solanaRPC(ctx, "getBalance", []any{address, map[string]any{"commitment": solanaCommitmentFinalized}}, &result); err != nil {
			return decimal.Zero, err
		}
		lamports, ok := decimalFromAny(result.Value)
		if !ok {
			return decimal.Zero, nil
		}
		return decimalFromBaseUnits(lamports, solanaNativeDecimals), nil
	}
	token, err := solanaTokenConfig(crypto)
	if err != nil {
		return decimal.Zero, err
	}
	mint, err := solanaPubKeyFromBase58(token.Mint)
	if err != nil {
		return decimal.Zero, err
	}
	var result struct {
		Value []struct {
			Account struct {
				Data struct {
					Parsed struct {
						Info struct {
							TokenAmount struct {
								Amount   string `json:"amount"`
								Decimals int    `json:"decimals"`
							} `json:"tokenAmount"`
						} `json:"info"`
					} `json:"parsed"`
				} `json:"data"`
			} `json:"account"`
		} `json:"value"`
	}
	if err := s.solanaRPC(ctx, "getTokenAccountsByOwner", []any{
		owner.String(),
		map[string]any{"mint": mint.String()},
		map[string]any{"encoding": "jsonParsed", "commitment": solanaCommitmentFinalized},
	}, &result); err != nil {
		return decimal.Zero, err
	}
	total := decimal.Zero
	for _, row := range result.Value {
		amount, err := decimal.NewFromString(strings.TrimSpace(row.Account.Data.Parsed.Info.TokenAmount.Amount))
		if err != nil {
			continue
		}
		decimals := row.Account.Data.Parsed.Info.TokenAmount.Decimals
		if decimals <= 0 {
			decimals = token.Decimals
		}
		total = total.Add(decimalFromBaseUnits(amount, int32(decimals)))
	}
	return total, nil
}

func (s *Server) broadcastSolanaPayout(ctx context.Context, crypto string, destination string, amount decimal.Decimal) (broadcastResult, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return broadcastResult{}, err
	}
	if len(accounts) == 0 && !s.isNative(crypto) {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
		if err != nil {
			return broadcastResult{}, err
		}
	}
	for _, account := range accounts {
		balance, err := s.solanaBalance(ctx, crypto, account.Address)
		if err != nil || balance.LessThan(amount) {
			continue
		}
		nativeRequired := decimal.RequireFromString("0.00001")
		if s.isNative(crypto) {
			nativeRequired = nativeRequired.Add(amount)
		} else {
			nativeRequired, err = decimal.NewFromString(env("SOLANA_TOKEN_MIN_FEE_SOL", "0.003"))
			if err != nil || nativeRequired.LessThan(decimal.Zero) {
				return broadcastResult{}, fmt.Errorf("invalid SOLANA_TOKEN_MIN_FEE_SOL")
			}
		}
		nativeBalance, err := s.solanaBalance(ctx, "SOL", account.Address)
		if err != nil || nativeBalance.LessThan(nativeRequired) {
			continue
		}
		txid, err := s.signAndBroadcastSolana(ctx, account, crypto, destination, amount)
		if err != nil {
			return broadcastResult{}, err
		}
		return broadcastResult{Dest: destination, TxIDs: []string{txid}, Status: "success"}, nil
	}
	return broadcastResult{}, fmt.Errorf("no %s account has enough balance and SOL fee reserve for payout", crypto)
}

func (s *Server) signAndBroadcastSolana(ctx context.Context, account Account, crypto string, destination string, amount decimal.Decimal) (string, error) {
	privateKeyEncoded, err := decryptSecret(s.cfg.AccountPassword, account.PrivateKeyHex)
	if err != nil {
		return "", err
	}
	privateKey, err := solanaPrivateKeyFromBase58(privateKeyEncoded)
	if err != nil {
		return "", err
	}
	from := solanaPubKeyFromPrivate(privateKey)
	to, err := solanaPubKeyFromBase58(destination)
	if err != nil {
		return "", err
	}
	blockhash, err := s.solanaLatestBlockhash(ctx)
	if err != nil {
		return "", err
	}
	var instructions []solanaInstruction
	if s.isNative(crypto) {
		lamports, err := amountToUint64BaseUnits(amount, solanaNativeDecimals)
		if err != nil {
			return "", err
		}
		instructions = []solanaInstruction{solanaNativeTransferInstruction(from, to, lamports)}
	} else {
		token, err := solanaTokenConfig(crypto)
		if err != nil {
			return "", err
		}
		mint, err := solanaPubKeyFromBase58(token.Mint)
		if err != nil {
			return "", err
		}
		tokenProgram, err := solanaPubKeyFromBase58(token.TokenProgram)
		if err != nil {
			return "", err
		}
		sourceATA, err := solanaAssociatedTokenAddress(from, mint, tokenProgram)
		if err != nil {
			return "", err
		}
		destinationATA, err := solanaAssociatedTokenAddress(to, mint, tokenProgram)
		if err != nil {
			return "", err
		}
		amountUnits, err := amountToUint64BaseUnits(amount, token.Decimals)
		if err != nil {
			return "", err
		}
		instructions = []solanaInstruction{
			solanaCreateAssociatedTokenInstruction(from, to, mint, destinationATA, tokenProgram),
			solanaTransferCheckedInstruction(sourceATA, mint, destinationATA, from, amountUnits, token.Decimals, tokenProgram),
		}
	}
	raw, fallbackSignature, err := solanaSignTransaction(privateKey, blockhash, instructions)
	if err != nil {
		return "", err
	}
	var txid string
	if err := s.solanaRPC(ctx, "sendTransaction", []any{
		base64.StdEncoding.EncodeToString(raw),
		map[string]any{"encoding": "base64", "skipPreflight": false, "preflightCommitment": "confirmed"},
	}, &txid); err != nil {
		return "", err
	}
	if txid == "" {
		txid = fallbackSignature
	}
	return txid, nil
}

func (s *Server) solanaTransfersByTx(ctx context.Context, crypto string, txid string) ([]transferResult, error) {
	latest, err := s.solanaLatestBlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.solanaAccountSet(ctx, crypto)
	if err != nil {
		return nil, err
	}
	tx, err := s.solanaTransaction(ctx, txid)
	if err != nil {
		return nil, err
	}
	if tx.Meta.Err != nil {
		return nil, fmt.Errorf("solana transaction failed: %v", tx.Meta.Err)
	}
	if s.isNative(crypto) {
		return solanaNativeTransfers(tx, latest, accounts), nil
	}
	token, err := solanaTokenConfig(crypto)
	if err != nil {
		return nil, err
	}
	return solanaTokenTransfers(tx, latest, accounts, token)
}

func (s *Server) solanaTransaction(ctx context.Context, txid string) (solanaTransactionResult, error) {
	var tx solanaTransactionResult
	err := s.solanaRPC(ctx, "getTransaction", []any{
		txid,
		map[string]any{"encoding": "json", "commitment": solanaCommitmentFinalized, "maxSupportedTransactionVersion": 0},
	}, &tx)
	if err != nil {
		return tx, err
	}
	if tx.Slot <= 0 {
		return tx, fmt.Errorf("solana transaction not found: %s", txid)
	}
	return tx, nil
}

func solanaNativeTransfers(tx solanaTransactionResult, latest int64, accounts map[string]struct{}) []transferResult {
	out := make([]transferResult, 0)
	limit := len(tx.Meta.PreBalances)
	if len(tx.Meta.PostBalances) < limit {
		limit = len(tx.Meta.PostBalances)
	}
	if len(tx.Transaction.Message.AccountKeys) < limit {
		limit = len(tx.Transaction.Message.AccountKeys)
	}
	for i := 0; i < limit; i++ {
		address := strings.ToLower(string(tx.Transaction.Message.AccountKeys[i]))
		if _, ok := accounts[address]; !ok {
			continue
		}
		pre, okPre := decimalFromAny(tx.Meta.PreBalances[i])
		post, okPost := decimalFromAny(tx.Meta.PostBalances[i])
		if !okPre || !okPost {
			continue
		}
		delta := post.Sub(pre)
		category := ""
		if delta.GreaterThan(decimal.Zero) {
			category = "receive"
		} else if delta.LessThan(decimal.Zero) {
			category = "send"
			delta = delta.Abs()
		}
		if category == "" {
			continue
		}
		out = append(out, transferResult{
			Address:       string(tx.Transaction.Message.AccountKeys[i]),
			Amount:        decimalFromBaseUnits(delta, solanaNativeDecimals).String(),
			Confirmations: confirmations(latest, tx.Slot),
			Category:      category,
		})
	}
	return out
}

func solanaTokenTransfers(tx solanaTransactionResult, latest int64, accounts map[string]struct{}, token solanaTokenDefinition) ([]transferResult, error) {
	pre := solanaTokenBalanceMap(tx.Meta.PreTokenBalances, token.Mint)
	post := solanaTokenBalanceMap(tx.Meta.PostTokenBalances, token.Mint)
	owners := solanaTokenOwnerMap(tx, token.Mint)
	indexes := map[int]struct{}{}
	for index := range pre {
		indexes[index] = struct{}{}
	}
	for index := range post {
		indexes[index] = struct{}{}
	}
	out := make([]transferResult, 0, len(indexes))
	for index := range indexes {
		owner := strings.ToLower(owners[index])
		accountAddress := ""
		if index >= 0 && index < len(tx.Transaction.Message.AccountKeys) {
			accountAddress = strings.ToLower(string(tx.Transaction.Message.AccountKeys[index]))
		}
		if _, ok := accounts[owner]; !ok {
			if _, ok := accounts[accountAddress]; !ok {
				continue
			}
		}
		delta := post[index].Sub(pre[index])
		category := ""
		if delta.GreaterThan(decimal.Zero) {
			category = "receive"
		} else if delta.LessThan(decimal.Zero) {
			category = "send"
			delta = delta.Abs()
		}
		if category == "" {
			continue
		}
		address := owners[index]
		if address == "" && index >= 0 && index < len(tx.Transaction.Message.AccountKeys) {
			address = string(tx.Transaction.Message.AccountKeys[index])
		}
		out = append(out, transferResult{
			Address:       address,
			Amount:        decimalFromBaseUnits(delta, int32(token.Decimals)).String(),
			Confirmations: confirmations(latest, tx.Slot),
			Category:      category,
		})
	}
	return out, nil
}

func solanaTokenBalanceMap(rows []solanaTokenBalance, mint string) map[int]decimal.Decimal {
	out := map[int]decimal.Decimal{}
	for _, row := range rows {
		if !strings.EqualFold(row.Mint, mint) {
			continue
		}
		amount, err := decimal.NewFromString(strings.TrimSpace(row.TokenAmount.Amount))
		if err != nil {
			continue
		}
		out[row.AccountIndex] = amount
	}
	return out
}

func solanaTokenOwnerMap(tx solanaTransactionResult, mint string) map[int]string {
	out := map[int]string{}
	for _, row := range append(tx.Meta.PreTokenBalances, tx.Meta.PostTokenBalances...) {
		if strings.EqualFold(row.Mint, mint) && row.Owner != "" {
			out[row.AccountIndex] = row.Owner
		}
	}
	return out
}

func (s *Server) solanaAccountSet(ctx context.Context, crypto string) (map[string]struct{}, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 && !s.isNative(crypto) {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
		if err != nil {
			return nil, err
		}
	}
	out := make(map[string]struct{}, len(accounts)*2)
	var token *solanaTokenDefinition
	if !s.isNative(crypto) {
		if cfg, err := solanaTokenConfig(crypto); err == nil {
			token = &cfg
		}
	}
	for _, account := range accounts {
		if account.Address == "" {
			continue
		}
		out[strings.ToLower(account.Address)] = struct{}{}
		if token == nil {
			continue
		}
		owner, err := solanaPubKeyFromBase58(account.Address)
		if err != nil {
			continue
		}
		mint, err := solanaPubKeyFromBase58(token.Mint)
		if err != nil {
			continue
		}
		tokenProgram, err := solanaPubKeyFromBase58(token.TokenProgram)
		if err != nil {
			continue
		}
		ata, err := solanaAssociatedTokenAddress(owner, mint, tokenProgram)
		if err == nil {
			out[strings.ToLower(ata.String())] = struct{}{}
		}
	}
	return out, nil
}

func (s *Server) solanaLatestBlockNumber(ctx context.Context) (int64, error) {
	var slot json.Number
	if err := s.solanaRPC(ctx, "getSlot", []any{map[string]any{"commitment": solanaCommitmentFinalized}}, &slot); err != nil {
		return 0, err
	}
	return int64FromAny(slot), nil
}

func (s *Server) solanaBlockTime(ctx context.Context, slot int64) (int64, error) {
	var timestamp json.Number
	if err := s.solanaRPC(ctx, "getBlockTime", []any{slot}, &timestamp); err != nil {
		return 0, err
	}
	value := int64FromAny(timestamp)
	if value <= 0 {
		return 0, errors.New("solana fullnode returned no block timestamp")
	}
	return value, nil
}

func (s *Server) solanaLatestBlockhash(ctx context.Context) (solanaPubKey, error) {
	var result struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := s.solanaRPC(ctx, "getLatestBlockhash", []any{map[string]any{"commitment": solanaCommitmentFinalized}}, &result); err != nil {
		return solanaPubKey{}, err
	}
	if result.Value.Blockhash == "" {
		return solanaPubKey{}, errors.New("solana fullnode returned no blockhash")
	}
	return solanaPubKeyFromBase58(result.Value.Blockhash)
}

func (s *Server) solanaRPC(ctx context.Context, method string, params []any, result any) error {
	endpoint := normalizeHTTPURL(s.cfg.FullnodeURL)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "go-chain-worker", "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.RPCUsername != "" || s.cfg.RPCPassword != "" {
		req.SetBasicAuth(s.cfg.RPCUsername, s.cfg.RPCPassword)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("solana rpc %s returned %d: %s", method, resp.StatusCode, string(data))
	}
	var payload struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return err
	}
	if payload.Error != nil {
		return fmt.Errorf("solana rpc %s error: %v", method, payload.Error)
	}
	if result == nil {
		return nil
	}
	dec = json.NewDecoder(bytes.NewReader(payload.Result))
	dec.UseNumber()
	return dec.Decode(result)
}

func solanaNativeTransferInstruction(from, to solanaPubKey, lamports uint64) solanaInstruction {
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[:4], 2)
	binary.LittleEndian.PutUint64(data[4:], lamports)
	return solanaInstruction{
		ProgramID: solanaSystemProgramID,
		Accounts: []solanaInstructionAccount{
			{PublicKey: from, Signer: true, Writable: true},
			{PublicKey: to, Writable: true},
		},
		Data: data,
	}
}

func solanaCreateAssociatedTokenInstruction(payer, wallet, mint, ata, tokenProgram solanaPubKey) solanaInstruction {
	return solanaInstruction{
		ProgramID: solanaAssociatedTokenID,
		Accounts: []solanaInstructionAccount{
			{PublicKey: payer, Signer: true, Writable: true},
			{PublicKey: ata, Writable: true},
			{PublicKey: wallet},
			{PublicKey: mint},
			{PublicKey: solanaSystemProgramID},
			{PublicKey: tokenProgram},
		},
		Data: []byte{1},
	}
}

func solanaTransferCheckedInstruction(sourceATA, mint, destinationATA, owner solanaPubKey, amount uint64, decimals int, tokenProgram solanaPubKey) solanaInstruction {
	data := make([]byte, 10)
	data[0] = 12
	binary.LittleEndian.PutUint64(data[1:9], amount)
	data[9] = byte(decimals)
	return solanaInstruction{
		ProgramID: tokenProgram,
		Accounts: []solanaInstructionAccount{
			{PublicKey: sourceATA, Writable: true},
			{PublicKey: mint},
			{PublicKey: destinationATA, Writable: true},
			{PublicKey: owner, Signer: true},
		},
		Data: data,
	}
}

func solanaSignTransaction(privateKey ed25519.PrivateKey, recentBlockhash solanaPubKey, instructions []solanaInstruction) ([]byte, string, error) {
	payer := solanaPubKeyFromPrivate(privateKey)
	accounts, accountIndexes := solanaCompileAccounts(payer, instructions)
	message := make([]byte, 0, 256)
	readonlySigned, readonlyUnsigned := solanaReadonlyCounts(accounts)
	message = append(message, 1, byte(readonlySigned), byte(readonlyUnsigned))
	message = append(message, solanaShortVec(len(accounts))...)
	for _, account := range accounts {
		message = append(message, account.PublicKey[:]...)
	}
	message = append(message, recentBlockhash[:]...)
	message = append(message, solanaShortVec(len(instructions))...)
	for _, instruction := range instructions {
		programIndex, ok := accountIndexes[instruction.ProgramID.String()]
		if !ok {
			return nil, "", errors.New("solana instruction program was not compiled")
		}
		message = append(message, byte(programIndex))
		message = append(message, solanaShortVec(len(instruction.Accounts))...)
		for _, account := range instruction.Accounts {
			index, ok := accountIndexes[account.PublicKey.String()]
			if !ok {
				return nil, "", errors.New("solana instruction account was not compiled")
			}
			message = append(message, byte(index))
		}
		message = append(message, solanaShortVec(len(instruction.Data))...)
		message = append(message, instruction.Data...)
	}
	signature := ed25519.Sign(privateKey, message)
	raw := make([]byte, 0, 1+len(signature)+len(message))
	raw = append(raw, solanaShortVec(1)...)
	raw = append(raw, signature...)
	raw = append(raw, message...)
	return raw, base58Encode(signature), nil
}

func solanaCompileAccounts(payer solanaPubKey, instructions []solanaInstruction) ([]solanaCompiledAccount, map[string]int) {
	byKey := map[string]*solanaCompiledAccount{}
	order := 0
	add := func(pubKey solanaPubKey, signer bool, writable bool) {
		key := pubKey.String()
		if existing, ok := byKey[key]; ok {
			existing.Signer = existing.Signer || signer
			existing.Writable = existing.Writable || writable
			return
		}
		byKey[key] = &solanaCompiledAccount{PublicKey: pubKey, Signer: signer, Writable: writable, Order: order}
		order++
	}
	add(payer, true, true)
	for _, instruction := range instructions {
		for _, account := range instruction.Accounts {
			add(account.PublicKey, account.Signer, account.Writable)
		}
		add(instruction.ProgramID, false, false)
	}
	ordered := make([]solanaCompiledAccount, 0, len(byKey))
	for _, account := range byKey {
		ordered = append(ordered, *account)
	}
	sortSolanaAccounts(ordered)
	indexes := make(map[string]int, len(ordered))
	for i, account := range ordered {
		indexes[account.PublicKey.String()] = i
	}
	return ordered, indexes
}

func sortSolanaAccounts(accounts []solanaCompiledAccount) {
	for i := 1; i < len(accounts); i++ {
		value := accounts[i]
		j := i - 1
		for j >= 0 && solanaAccountLess(value, accounts[j]) {
			accounts[j+1] = accounts[j]
			j--
		}
		accounts[j+1] = value
	}
}

func solanaAccountLess(a, b solanaCompiledAccount) bool {
	if a.Signer != b.Signer {
		return a.Signer
	}
	if a.Writable != b.Writable {
		return a.Writable
	}
	return a.Order < b.Order
}

func solanaReadonlyCounts(accounts []solanaCompiledAccount) (signed int, unsigned int) {
	for _, account := range accounts {
		if account.Writable {
			continue
		}
		if account.Signer {
			signed++
		} else {
			unsigned++
		}
	}
	return signed, unsigned
}

func solanaShortVec(value int) []byte {
	out := make([]byte, 0, 2)
	for {
		elem := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			out = append(out, elem)
			return out
		}
		out = append(out, elem|0x80)
	}
}

func solanaAssociatedTokenAddress(wallet, mint, tokenProgram solanaPubKey) (solanaPubKey, error) {
	pubKey, _, err := solanaFindProgramAddress([][]byte{wallet[:], tokenProgram[:], mint[:]}, solanaAssociatedTokenID)
	return pubKey, err
}

func solanaFindProgramAddress(seeds [][]byte, program solanaPubKey) (solanaPubKey, byte, error) {
	for bump := 255; bump >= 0; bump-- {
		withBump := append(append([][]byte{}, seeds...), []byte{byte(bump)})
		pubKey, err := solanaCreateProgramAddress(withBump, program)
		if err == nil {
			return pubKey, byte(bump), nil
		}
	}
	return solanaPubKey{}, 0, errors.New("unable to find a viable solana program address")
}

func solanaCreateProgramAddress(seeds [][]byte, program solanaPubKey) (solanaPubKey, error) {
	h := sha256.New()
	for _, seed := range seeds {
		if len(seed) > 32 {
			return solanaPubKey{}, errors.New("solana PDA seed is too long")
		}
		_, _ = h.Write(seed)
	}
	_, _ = h.Write(program[:])
	_, _ = h.Write([]byte(solanaPDAConstant))
	sum := h.Sum(nil)
	if _, err := new(edwards25519.Point).SetBytes(sum); err == nil {
		return solanaPubKey{}, errors.New("solana PDA candidate is on curve")
	}
	var out solanaPubKey
	copy(out[:], sum)
	return out, nil
}

func solanaTokenConfig(crypto string) (solanaTokenDefinition, error) {
	normalized := strings.ReplaceAll(strings.ToUpper(crypto), "-", "_")
	symbol := strings.TrimPrefix(normalized, "SOLANA_")
	def, hasDefault := defaultSolanaTokenConfig(crypto)
	mint := firstEnv(normalized+"_MINT", "SOLANA_"+symbol+"_MINT")
	tokenProgram := firstEnv(normalized+"_TOKEN_PROGRAM", "SOLANA_"+symbol+"_TOKEN_PROGRAM")
	if mint != "" {
		decimalsDefault := int64(0)
		if hasDefault {
			decimalsDefault = int64(def.Decimals)
		}
		decimals := int(int64Env(normalized+"_DECIMALS", int64Env("SOLANA_"+symbol+"_DECIMALS", decimalsDefault)))
		if decimals <= 0 {
			return solanaTokenDefinition{}, fmt.Errorf("solana token decimals are not configured for %s", crypto)
		}
		if tokenProgram == "" {
			tokenProgram = solanaTokenProgramBase58
		}
		return solanaTokenDefinition{Mint: mint, Decimals: decimals, TokenProgram: tokenProgram}, nil
	}
	if !hasDefault {
		return solanaTokenDefinition{}, fmt.Errorf("solana token mint is not configured for %s", crypto)
	}
	if tokenProgram != "" {
		def.TokenProgram = tokenProgram
	}
	return def, nil
}

func defaultSolanaTokenConfig(crypto string) (solanaTokenDefinition, bool) {
	switch strings.ToUpper(crypto) {
	case "SOLANA-USDT":
		return solanaTokenDefinition{Mint: "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", Decimals: 6, TokenProgram: solanaTokenProgramBase58}, true
	case "SOLANA-USDC":
		return solanaTokenDefinition{Mint: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", Decimals: 6, TokenProgram: solanaTokenProgramBase58}, true
	case "SOLANA-PYUSD":
		return solanaTokenDefinition{Mint: "2b1kV6DkPAnxd5ixfnxCpjxmKwqjjaYmCZfHsFu24GXo", Decimals: 6, TokenProgram: solanaToken2022Base58}, true
	default:
		return solanaTokenDefinition{}, false
	}
}

func amountToUint64BaseUnits(amount decimal.Decimal, decimals int) (uint64, error) {
	value := amountToBaseUnits(amount, decimals)
	if value == nil || value.Sign() < 0 || !value.IsUint64() {
		return 0, fmt.Errorf("amount is outside uint64 base-unit range: %s", amount)
	}
	return value.Uint64(), nil
}

func solanaPrivateKeyFromBase58(value string) (ed25519.PrivateKey, error) {
	raw, err := base58Decode(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	default:
		return nil, fmt.Errorf("invalid solana private key length: %d", len(raw))
	}
}

func solanaPubKeyFromPrivate(privateKey ed25519.PrivateKey) solanaPubKey {
	public := privateKey.Public().(ed25519.PublicKey)
	var out solanaPubKey
	copy(out[:], public)
	return out
}

func solanaPubKeyFromBase58(value string) (solanaPubKey, error) {
	raw, err := base58Decode(strings.TrimSpace(value))
	if err != nil {
		return solanaPubKey{}, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return solanaPubKey{}, fmt.Errorf("invalid solana public key length: %d", len(raw))
	}
	var out solanaPubKey
	copy(out[:], raw)
	return out, nil
}

func solanaConstPubKey(value string) solanaPubKey {
	pubKey, _ := solanaPubKeyFromBase58(value)
	return pubKey
}

func (p solanaPubKey) String() string {
	return base58Encode(p[:])
}

func solanaTokenProgramIs2022(program string) bool {
	return strings.EqualFold(strings.TrimSpace(program), solanaToken2022Base58)
}

func solanaEnvDefaultTokenProgram(crypto string) string {
	token, err := solanaTokenConfig(crypto)
	if err != nil {
		return ""
	}
	if solanaTokenProgramIs2022(token.TokenProgram) {
		return solanaToken2022Base58
	}
	return solanaTokenProgramBase58
}

func solanaMintEnvKey(crypto string) string {
	return strings.ReplaceAll(strings.ToUpper(crypto), "-", "_") + "_MINT"
}

func solanaDecimalsEnvKey(crypto string) string {
	return strings.ReplaceAll(strings.ToUpper(crypto), "-", "_") + "_DECIMALS"
}

func solanaTokenProgramEnvKey(crypto string) string {
	return strings.ReplaceAll(strings.ToUpper(crypto), "-", "_") + "_TOKEN_PROGRAM"
}

func solanaEnvValue(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func bigIntFromJSONNumber(n json.Number) *big.Int {
	value := new(big.Int)
	if _, ok := value.SetString(string(n), 10); ok {
		return value
	}
	return big.NewInt(0)
}
