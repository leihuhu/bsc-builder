package miner

import (
	"errors"
	"math/big"
	"sort"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

const smallBundleGas = 10 * params.TxGas

var (
	errNonRevertingTxInBundleFailed = errors.New("non-reverting tx in bundle failed")
	errBundlePriceTooLow            = errors.New("bundle price too low")
)

func (w *worker) fillTransactionsAndBundles(interruptCh chan int32, env *environment, stopTimer *time.Timer) error {
	env.state.StopPrefetcher()

	fullGasLimit := env.header.GasLimit
	env.header.GasLimit /= 2
	defer func() { env.header.GasLimit = fullGasLimit }()

	{
		bundles := w.eth.TxPool().PendingBundles(env.header.Number.Uint64(), env.header.Time)
		if len(bundles) > 0 {
			txs, bundle, err := w.generateOrderedBundles(env, bundles)
			if err != nil {
				log.Error("fail to generate ordered bundles", "err", err)
				return err
			}
			if err = w.commitBundles(env, txs, interruptCh, stopTimer); err != nil {
				log.Error("test: fail to commit bundles", "err", err)
				return err
			}
			env.profit.Add(env.profit, bundle.EthSentToSystem)
			log.Info("test: fill bundles", "bundles_count", len(bundles))
		}
	}

	{
		w.confMu.RLock()
		tip := w.tip
		prio := w.prio
		w.confMu.RUnlock()
		filter := txpool.PendingFilter{MinTip: tip}
		if env.header.BaseFee != nil {
			filter.BaseFee = uint256.MustFromBig(env.header.BaseFee)
		}
		if env.header.ExcessBlobGas != nil {
			filter.BlobFee = uint256.MustFromBig(eip4844.CalcBlobFee(w.chainConfig, env.header))
		}
		filter.OnlyPlainTxs, filter.OnlyBlobTxs = true, false
		pendingPlainTxs := w.eth.TxPool().Pending(filter)
		filter.OnlyPlainTxs, filter.OnlyBlobTxs = false, true
		pendingBlobTxs := w.eth.TxPool().Pending(filter)

		prioPlainTxs, normalPlainTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingPlainTxs
		prioBlobTxs, normalBlobTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingBlobTxs
		for _, account := range prio {
			if txs := normalPlainTxs[account]; len(txs) > 0 {
				delete(normalPlainTxs, account)
				prioPlainTxs[account] = txs
			}
			if txs := normalBlobTxs[account]; len(txs) > 0 {
				delete(normalBlobTxs, account)
				prioBlobTxs[account] = txs
			}
		}

		if len(prioPlainTxs) > 0 || len(prioBlobTxs) > 0 {
			plainTxs := newTransactionsByPriceAndNonce(env.signer, prioPlainTxs, env.header.BaseFee)
			blobTxs := newTransactionsByPriceAndNonce(env.signer, prioBlobTxs, env.header.BaseFee)
			if err := w.commitTransactions(env, plainTxs, blobTxs, interruptCh, stopTimer); err != nil {
				return err
			}
		}
		if len(normalPlainTxs) > 0 || len(normalBlobTxs) > 0 {
			plainTxs := newTransactionsByPriceAndNonce(env.signer, normalPlainTxs, env.header.BaseFee)
			blobTxs := newTransactionsByPriceAndNonce(env.signer, normalBlobTxs, env.header.BaseFee)
			if err := w.commitTransactions(env, plainTxs, blobTxs, interruptCh, stopTimer); err != nil {
				return err
			}
		}
		log.Info("fill transactions", "plain_txs_count", len(prioPlainTxs)+len(normalPlainTxs), "blob_txs_count", len(prioBlobTxs)+len(normalBlobTxs))
	}

	log.Info("test: fill bundles and transactions done", "total_txs_count", len(env.txs))
	return nil
}

func (w *worker) commitBundles(env *environment, txs types.Transactions, interruptCh chan int32, stopTimer *time.Timer) error {
	if env.gasPool == nil {
		env.gasPool = prepareGasPool(env.header.GasLimit)
	}
	var coalescedLogs []*types.Log
	signal := commitInterruptNone
LOOP:
	for _, tx := range txs {
		if interruptCh != nil {
			select {
			case signal, ok := <-interruptCh:
				if !ok {
					log.Warn("commit transactions stopped unknown")
				}
				return signalToErr(signal)
			default:
			}
		}
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			signal = commitInterruptOutOfGas
			break
		}
		if tx == nil {
			log.Error("Unexpected nil transaction in bundle")
			return signalToErr(commitInterruptBundleTxNil)
		}
		if stopTimer != nil {
			select {
			case <-stopTimer.C:
				log.Info("Not enough time for further transactions", "txs", len(env.txs))
				stopTimer.Reset(0)
				signal = commitInterruptTimeout
				break LOOP
			default:
			}
		}
		from, _ := types.Sender(env.signer, tx)
		if tx.Protected() && !w.chainConfig.IsEIP155(env.header.Number) {
			log.Debug("Unexpected protected transaction in bundle")
			return signalToErr(commitInterruptBundleTxProtected)
		}
		env.state.SetTxContext(tx.Hash(), env.tcount)
		logs, err := w.commitTransaction(env, tx, core.NewReceiptBloomGenerator())
		switch err {
		case core.ErrGasLimitReached:
			log.Error("Unexpected gas limit exceeded for current block in the bundle", "sender", from)
			return signalToErr(commitInterruptBundleCommit)
		case core.ErrNonceTooLow:
			log.Error("Transaction with low nonce in the bundle", "sender", from, "nonce", tx.Nonce())
			return signalToErr(commitInterruptBundleCommit)
		case core.ErrNonceTooHigh:
			log.Error("Account with high nonce in the bundle", "sender", from, "nonce", tx.Nonce())
			return signalToErr(commitInterruptBundleCommit)
		case nil:
			coalescedLogs = append(coalescedLogs, logs...)
			env.tcount++
			continue
		default:
			log.Error("Transaction failed in the bundle", "hash", tx.Hash(), "err", err)
			return signalToErr(commitInterruptBundleCommit)
		}
	}
	// pendingLogsFeed not available in this worker variant; skip sending
	return signalToErr(signal)
}

func (w *worker) generateOrderedBundles(env *environment, bundles []*types.Bundle) (types.Transactions, *types.SimulatedBundle, error) {
	sort.SliceStable(bundles, func(i, j int) bool {
		priceI, priceJ := bundles[i].Price, bundles[j].Price
		return priceI.Cmp(priceJ) >= 0
	})
	simulatedBundles, err := w.simulateBundles(env, bundles)
	if err != nil {
		log.Error("fail to simulate bundles base on the same state", "err", err)
		return nil, nil, err
	}
	sort.SliceStable(simulatedBundles, func(i, j int) bool {
		priceI, priceJ := simulatedBundles[i].BundleGasPrice, simulatedBundles[j].BundleGasPrice
		return priceI.Cmp(priceJ) >= 0
	})
	includedTxs, mergedBundle, err := w.mergeBundles(env, simulatedBundles)
	if err != nil {
		log.Error("fail to merge bundles", "err", err)
		return nil, nil, err
	}
	return includedTxs, mergedBundle, nil
}

func (w *worker) simulateBundles(env *environment, bundles []*types.Bundle) ([]*types.SimulatedBundle, error) {
	simulatedBundles := make([]*types.SimulatedBundle, 0, len(bundles))
	for _, bundle := range bundles {
		gasPool := prepareGasPool(env.header.GasLimit)
		evm := vm.NewEVM(core.NewEVMBlockContext(env.header, w.chain, &env.coinbase), env.state.Copy(), w.chainConfig, vm.Config{})
		simmed, err := w.simulateBundle(evm, env.header, bundle, env.state.Copy(), gasPool, 0, true, true)
		if err != nil {
			log.Trace("Error computing gas for a simulateBundle", "error", err)
			continue
		}
		if simmed != nil {
			simulatedBundles = append(simulatedBundles, simmed)
		}
	}
	return simulatedBundles, nil
}

func (w *worker) mergeBundles(env *environment, bundles []*types.SimulatedBundle) (types.Transactions, *types.SimulatedBundle, error) {
	currentState := env.state.Copy()
	gasPool := prepareGasPool(env.header.GasLimit)
	includedTxs := types.Transactions{}
	mergedBundle := types.SimulatedBundle{BundleGasFees: new(big.Int), BundleGasUsed: 0, BundleGasPrice: new(big.Int), EthSentToSystem: new(big.Int)}
	evm := vm.NewEVM(core.NewEVMBlockContext(env.header, w.chain, &env.coinbase), env.state.Copy(), w.chainConfig, vm.Config{})
	for _, bundle := range bundles {
		if gasPool.Gas() < smallBundleGas {
			break
		}
		prevState := currentState.Copy()
		prevGasPool := new(core.GasPool).AddGas(gasPool.Gas())
		floorGasPrice := new(big.Int).Mul(bundle.BundleGasPrice, big.NewInt(99))
		floorGasPrice = floorGasPrice.Div(floorGasPrice, big.NewInt(100))
		simulatedBundle, err := w.simulateBundle(evm, env.header, bundle.OriginalBundle, currentState, gasPool, len(includedTxs), true, false)
		if err != nil || simulatedBundle.BundleGasPrice.Cmp(floorGasPrice) <= 0 {
			currentState = prevState
			gasPool = prevGasPool
			log.Error("failed to merge bundle", "floorGasPrice", floorGasPrice.String(), "err", err)
			continue
		}
		includedTxs = append(includedTxs, bundle.OriginalBundle.Txs...)
		mergedBundle.BundleGasFees.Add(mergedBundle.BundleGasFees, simulatedBundle.BundleGasFees)
		mergedBundle.BundleGasUsed += simulatedBundle.BundleGasUsed
		for _, tx := range bundle.OriginalBundle.Txs {
			if !containsHash(bundle.OriginalBundle.RevertingTxHashes, tx.Hash()) {
				env.UnRevertible = append(env.UnRevertible, tx.Hash())
			}
		}
		log.Info("included bundle", "gasUsed", simulatedBundle.BundleGasUsed, "gasPrice", simulatedBundle.BundleGasPrice, "txcount", len(simulatedBundle.OriginalBundle.Txs), "unrevertible", len(env.UnRevertible))
	}
	if len(includedTxs) == 0 {
		return nil, nil, errors.New("include no txs when merge bundles")
	}
	mergedBundle.BundleGasPrice.Div(mergedBundle.BundleGasFees, new(big.Int).SetUint64(mergedBundle.BundleGasUsed))
	return includedTxs, &mergedBundle, nil
}

func (w *worker) simulateBundle(evm *vm.EVM, header *types.Header, bundle *types.Bundle, state *state.StateDB, gasPool *core.GasPool, currentTxCount int, prune, pruneGasExceed bool) (*types.SimulatedBundle, error) {
	var tempGasUsed uint64
	var bundleGasUsed uint64
	bundleGasFees := new(big.Int)
	ethSentToSystem := new(big.Int)
	txsLen := len(bundle.Txs)
	for i := 0; i < txsLen; i++ {
		tx := bundle.Txs[i]
		state.SetTxContext(tx.Hash(), i+currentTxCount)
		sysBalanceBefore := state.GetBalance(consensus.SystemAddress)
		snap := state.Snapshot()
		gp := gasPool.Gas()
		receipt, err := core.ApplyTransaction(evm, gasPool, state, header, tx, &tempGasUsed)
		if err != nil {
			log.Warn("fail to simulate bundle", "hash", bundle.Hash().String(), "err", err)
			if containsHash(bundle.DroppingTxHashes, tx.Hash()) {
				log.Warn("drop tx in bundle", "hash", tx.Hash().String())
				state.RevertToSnapshot(snap)
				gasPool.SetGas(gp)
				bundle.Txs = bundle.Txs.Remove(i)
				txsLen = len(bundle.Txs)
				i--
				continue
			}
			if prune {
				if errors.Is(err, core.ErrGasLimitReached) && !pruneGasExceed {
					log.Warn("bundle gas limit exceed", "hash", bundle.Hash().String())
				} else {
					log.Warn("prune bundle", "hash", bundle.Hash().String(), "err", err)
					w.eth.TxPool().PruneBundle(bundle.Hash())
				}
			}
			return nil, err
		}
		if receipt.Status == types.ReceiptStatusFailed && !containsHash(bundle.RevertingTxHashes, receipt.TxHash) {
			if containsHash(bundle.DroppingTxHashes, receipt.TxHash) {
				log.Warn("drop tx in bundle", "hash", receipt.TxHash.String())
				gasPool.SetGas(gp)
				bundle.Txs = bundle.Txs.Remove(i)
				txsLen = len(bundle.Txs)
				i--
				continue
			}
			err = errNonRevertingTxInBundleFailed
			log.Warn("fail to simulate bundle", "hash", bundle.Hash().String(), "err", err)
			if prune {
				w.eth.TxPool().PruneBundle(bundle.Hash())
				log.Warn("prune bundle", "hash", bundle.Hash().String())
			}
			return nil, err
		}
		if !w.eth.TxPool().Has(tx.Hash()) {
			bundleGasUsed += receipt.GasUsed
			txGasUsed := new(big.Int).SetUint64(receipt.GasUsed)
			effectiveTip, er := tx.EffectiveGasTip(header.BaseFee)
			if er != nil {
				return nil, er
			}
			if header.BaseFee != nil {
				effectiveTip.Add(effectiveTip, header.BaseFee)
			}
			txGasFees := new(big.Int).Mul(txGasUsed, effectiveTip)
			if tx.Type() == types.BlobTxType {
				blobFee := new(big.Int).SetUint64(receipt.BlobGasUsed)
				blobFee.Mul(blobFee, receipt.BlobGasPrice)
				txGasFees.Add(txGasFees, blobFee)
			}
			bundleGasFees.Add(bundleGasFees, txGasFees)
			sysBalanceAfter := state.GetBalance(consensus.SystemAddress)
			sysDelta := new(uint256.Int).Sub(sysBalanceAfter, sysBalanceBefore)
			sysDelta.Sub(sysDelta, uint256.MustFromBig(txGasFees))
			ethSentToSystem.Add(ethSentToSystem, sysDelta.ToBig())
		}
	}
	if len(bundle.Txs) == 0 {
		log.Warn("prune bundle", "hash", bundle.Hash().String(), "err", "empty bundle")
		w.eth.TxPool().PruneBundle(bundle.Hash())
		return nil, errors.New("empty bundle")
	}
	bundleGasPrice := big.NewInt(0)
	if bundleGasUsed != 0 {
		bundleGasPrice = new(big.Int).Div(bundleGasFees, new(big.Int).SetUint64(bundleGasUsed))
	}
	return &types.SimulatedBundle{OriginalBundle: bundle, BundleGasFees: bundleGasFees, BundleGasPrice: bundleGasPrice, BundleGasUsed: bundleGasUsed, EthSentToSystem: ethSentToSystem}, nil
}

func (w *worker) simulateGaslessBundle(env *environment, bundle *types.Bundle) (*types.SimulateGaslessBundleResp, error) {
	validResults := make([]types.GaslessTxSimResult, 0)
	gasReachedResults := make([]types.GaslessTxSimResult, 0)
	txIdx := 0
	for _, tx := range bundle.Txs {
		env.state.SetTxContext(tx.Hash(), txIdx)
		snap := env.state.Snapshot()
		gpVal := env.gasPool.Gas()
		receipt, err := core.ApplyTransaction(env.evm, env.gasPool, env.state, env.header, tx, &env.header.GasUsed)
		if err != nil {
			env.state.RevertToSnapshot(snap)
			env.gasPool.SetGas(gpVal)
			log.Error("fail to simulate gasless tx, skipped", "hash", tx.Hash(), "err", err)
			if err == core.ErrGasLimitReached {
				gasReachedResults = append(gasReachedResults, types.GaslessTxSimResult{Hash: tx.Hash()})
			}
		} else {
			txIdx++
			validResults = append(validResults, types.GaslessTxSimResult{Hash: tx.Hash(), GasUsed: receipt.GasUsed})
		}
	}
	return &types.SimulateGaslessBundleResp{ValidResults: validResults, GasReachedResults: gasReachedResults, BasedBlockNumber: env.header.Number.Int64()}, nil
}

func containsHash(arr []common.Hash, match common.Hash) bool {
	for _, elem := range arr {
		if elem == match {
			return true
		}
	}
	return false
}
func prepareGasPool(gasLimit uint64) *core.GasPool {
	gasPool := new(core.GasPool).AddGas(gasLimit)
	gasPool.SubGas(params.SystemTxsGasSoftLimit)
	return gasPool
}
