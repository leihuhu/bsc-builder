package bundlepool

import (
    "container/heap"
    "errors"
    "math/big"
    "sync"
    "time"

    mapset "github.com/deckarep/golang-set/v2"

    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/core"
    "github.com/ethereum/go-ethereum/core/state"
    "github.com/ethereum/go-ethereum/core/txpool"
    "github.com/ethereum/go-ethereum/core/types"
    "github.com/ethereum/go-ethereum/crypto/kzg4844"
    "github.com/ethereum/go-ethereum/event"
    "github.com/ethereum/go-ethereum/log"
    "github.com/ethereum/go-ethereum/metrics"
    "github.com/ethereum/go-ethereum/params"
)

const (
    bundleSlotSize         = 128 * 1024
    maxMinTimestampFromNow = int64(300)
)

var (
    bundleGauge = metrics.NewRegisteredGauge("bundlepool/bundles", nil)
    slotsGauge  = metrics.NewRegisteredGauge("bundlepool/slots", nil)
)

var (
    ErrSimulatorMissing      = errors.New("bundle simulator is missing")
    ErrBundleTimestampTooHigh = errors.New("bundle MinTimestamp is too high")
    ErrBundleGasPriceLow     = errors.New("bundle gas price is too low")
    ErrBundleAlreadyExist    = errors.New("bundle already exist")
)

type BlockChain interface {
    Config() *params.ChainConfig
    CurrentBlock() *types.Header
    GetBlock(hash common.Hash, number uint64) *types.Block
    StateAt(root common.Hash) (*state.StateDB, error)
}

type BundleSimulator interface {
    SimulateBundle(bundle *types.Bundle) (*big.Int, error)
}

type BundlePool struct {
    config Config

    bundles    map[common.Hash]*types.Bundle
    bundleHeap BundleHeap
    mu         sync.RWMutex

    slots uint64

    bundleMetrics   map[int64][][]common.Hash
    bundleMetricsMu sync.RWMutex

    simulator  BundleSimulator
    blockchain BlockChain
}

func (p *BundlePool) GetBlobs(vhashes []common.Hash) ([]*kzg4844.Blob, []*kzg4844.Proof) { return nil, nil }
func (p *BundlePool) Clear() {}

func New(config Config, chain BlockChain) *BundlePool {
    config = (&config).sanitize()
    pool := &BundlePool{
        config:        config,
        bundles:       make(map[common.Hash]*types.Bundle),
        bundleHeap:    make(BundleHeap, 0),
        blockchain:    chain,
        bundleMetrics: make(map[int64][][]common.Hash),
    }
    go pool.clearLoop()
    return pool
}

func (p *BundlePool) clearLoop() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        currentNumber := p.blockchain.CurrentBlock().Number.Int64()
        for number := range p.bundleMetrics {
            if number <= currentNumber-types.MaxBundleAliveBlock {
                p.bundleMetricsMu.Lock()
                delete(p.bundleMetrics, number)
                p.bundleMetricsMu.Unlock()
            }
        }
    }
}

func (p *BundlePool) SetBundleSimulator(simulator BundleSimulator) { p.simulator = simulator }
func (p *BundlePool) Init(gasTip uint64, head *types.Header, reserve txpool.Reserver) error { return nil }

func (p *BundlePool) FilterBundle(bundle *types.Bundle) bool {
    for _, tx := range bundle.Txs {
        if !p.filter(tx) {
            return false
        }
    }
    return true
}

func (p *BundlePool) AddBundle(bundle *types.Bundle) error {
    if p.simulator == nil {
        return ErrSimulatorMissing
    }
    if bundle.MinTimestamp > uint64(time.Now().Unix()+maxMinTimestampFromNow) {
        return ErrBundleTimestampTooHigh
    }
    price, err := p.simulator.SimulateBundle(bundle)
    if err != nil {
        log.Debug("simulation failed when add bundle into bundlepool", "err", err)
        return err
    }
    bundle.Price = price
    hash := bundle.Hash()
    p.mu.Lock()
    defer p.mu.Unlock()
    if _, ok := p.bundles[hash]; ok {
        return ErrBundleAlreadyExist
    }
    slotsNeeded := (bundle.Size() + bundleSlotSize - 1) / bundleSlotSize
    if price.Cmp(p.minimalBundleGasPrice()) < 0 && p.slots+slotsNeeded > p.config.GlobalSlots {
        return ErrBundleGasPriceLow
    }
    for p.slots+slotsNeeded > p.config.GlobalSlots {
        p.drop()
    }
    p.bundles[hash] = bundle
    heap.Push(&p.bundleHeap, bundle)
    p.slots += slotsNeeded
    bundleGauge.Update(int64(len(p.bundles)))
    slotsGauge.Update(int64(p.slots))
    p.bundleMetricsMu.Lock()
    defer p.bundleMetricsMu.Unlock()
    currentHeaderNumber := p.blockchain.CurrentBlock().Number.Int64()
    p.bundleMetrics[currentHeaderNumber] = append(p.bundleMetrics[currentHeaderNumber], bundle.TxHashes())
    return nil
}

func (p *BundlePool) BundleMetrics(fromBlock, toBlock int64) map[int64][][]common.Hash {
    p.bundleMetricsMu.RLock()
    defer p.bundleMetricsMu.RUnlock()
    ret := make(map[int64][][]common.Hash)
    for number := fromBlock; number <= toBlock; number++ {
        if bundles, ok := p.bundleMetrics[number]; ok {
            ret[number] = bundles
        }
    }
    return ret
}

func (p *BundlePool) GetBundle(hash common.Hash) *types.Bundle {
    p.mu.RLock()
    defer p.mu.RUnlock()
    return p.bundles[hash]
}

func (p *BundlePool) PruneBundle(hash common.Hash) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.deleteBundle(hash)
}

func (p *BundlePool) PendingBundles(blockNumber uint64, blockTimestamp uint64) []*types.Bundle {
    p.mu.Lock()
    defer p.mu.Unlock()
    ret := make([]*types.Bundle, 0)
    for hash, bundle := range p.bundles {
        if (bundle.MaxTimestamp != 0 && blockTimestamp > bundle.MaxTimestamp) || (bundle.MaxBlockNumber != 0 && blockNumber > bundle.MaxBlockNumber) {
            p.deleteBundle(hash)
            continue
        }
        if bundle.MinTimestamp != 0 && blockTimestamp < bundle.MinTimestamp {
            continue
        }
        ret = append(ret, bundle)
    }
    bundleGauge.Update(int64(len(p.bundles)))
    slotsGauge.Update(int64(p.slots))
    return ret
}

func (p *BundlePool) AllBundles() []*types.Bundle {
    p.mu.RLock()
    defer p.mu.RUnlock()
    bundles := make([]*types.Bundle, 0, len(p.bundles))
    for _, b := range p.bundles {
        bundles = append(bundles, b)
    }
    return bundles
}

func (p *BundlePool) Filter(tx *types.Transaction) bool { return false }
func (p *BundlePool) Close() error { log.Info("Bundle pool stopped"); return nil }
func (p *BundlePool) Reset(oldHead, newHead *types.Header) { p.reset(newHead) }
func (p *BundlePool) SetGasTip(tip *big.Int) {}
func (p *BundlePool) SetMaxGas(maxGas uint64) {}
func (p *BundlePool) Has(hash common.Hash) bool { return false }
func (p *BundlePool) Get(hash common.Hash) *types.Transaction { return nil }
func (p *BundlePool) GetRLP(hash common.Hash) []byte { return nil }
func (p *BundlePool) GetMetadata(hash common.Hash) *txpool.TxMetadata { return nil }
func (p *BundlePool) ValidateTxBasics(tx *types.Transaction) error { return nil }
func (p *BundlePool) Add(txs []*types.Transaction, sync bool, private bool) []error { return nil }
func (p *BundlePool) Pending(filter txpool.PendingFilter) map[common.Address][]*txpool.LazyTransaction { return nil }
func (p *BundlePool) IsPrivateTxHash(hash common.Hash) bool { return false }
func (p *BundlePool) SubscribeTransactions(ch chan<- core.NewTxsEvent, reorgs bool) event.Subscription { return nil }
func (p *BundlePool) SubscribeReannoTxsEvent(chan<- core.ReannoTxsEvent) event.Subscription { return nil }
func (p *BundlePool) Nonce(addr common.Address) uint64 { return 0 }
func (p *BundlePool) Stats() (int, int) { return 0, 0 }
func (p *BundlePool) Content() (map[common.Address][]*types.Transaction, map[common.Address][]*types.Transaction) { return make(map[common.Address][]*types.Transaction), make(map[common.Address][]*types.Transaction) }
func (p *BundlePool) ContentFrom(addr common.Address) ([]*types.Transaction, []*types.Transaction) { return []*types.Transaction{}, []*types.Transaction{} }
func (p *BundlePool) Locals() []common.Address { return []common.Address{} }
func (p *BundlePool) Status(hash common.Hash) txpool.TxStatus { return txpool.TxStatusUnknown }

func (p *BundlePool) filter(tx *types.Transaction) bool {
    switch tx.Type() {
    case types.LegacyTxType, types.AccessListTxType, types.DynamicFeeTxType, types.SetCodeTxType:
        return true
    default:
        return false
    }
}

func (p *BundlePool) reset(newHead *types.Header) {
    p.mu.Lock()
    defer p.mu.Unlock()
    if len(p.bundles) == 0 {
        bundleGauge.Update(int64(len(p.bundles)))
        slotsGauge.Update(int64(p.slots))
        return
    }
    block := p.blockchain.GetBlock(newHead.Hash(), newHead.Number.Uint64())
    txSet := mapset.NewSet[common.Hash]()
    if block != nil {
        txs := block.Transactions()
        for _, tx := range txs { txSet.Add(tx.Hash()) }
    }
    p.bundleHeap = make(BundleHeap, 0)
    for hash, bundle := range p.bundles {
        if (bundle.MaxTimestamp != 0 && newHead.Time > bundle.MaxTimestamp) || (bundle.MaxBlockNumber != 0 && newHead.Number.Cmp(new(big.Int).SetUint64(bundle.MaxBlockNumber)) > 0) {
            slots := (p.bundles[hash].Size() + bundleSlotSize - 1) / bundleSlotSize
            p.slots -= slots
            delete(p.bundles, hash)
        } else {
            for _, tx := range bundle.Txs {
                if txSet.Contains(tx.Hash()) && !containsHash(bundle.DroppingTxHashes, tx.Hash()) {
                    slots := (p.bundles[hash].Size() + bundleSlotSize - 1) / bundleSlotSize
                    p.slots -= slots
                    delete(p.bundles, hash)
                    break
                }
            }
        }
        if p.bundles[hash] != nil {
            heap.Push(&p.bundleHeap, bundle)
        }
    }
}

func containsHash(arr []common.Hash, match common.Hash) bool {
    for _, elem := range arr {
        if elem == match {
            return true
        }
    }
    return false
}

func (p *BundlePool) deleteBundle(hash common.Hash) {
    if p.bundles[hash] == nil {
        return
    }
    slots := (p.bundles[hash].Size() + bundleSlotSize - 1) / bundleSlotSize
    p.slots -= slots
    delete(p.bundles, hash)
}

func (p *BundlePool) drop() {
    for len(p.bundleHeap) > 0 {
        least := heap.Pop(&p.bundleHeap).(*types.Bundle).Hash()
        if _, ok := p.bundles[least]; ok {
            p.deleteBundle(least)
            break
        }
    }
}

func (p *BundlePool) minimalBundleGasPrice() *big.Int {
    for len(p.bundleHeap) != 0 {
        least := p.bundleHeap[0].Hash()
        if bundle, ok := p.bundles[least]; ok {
            return bundle.Price
        }
        heap.Pop(&p.bundleHeap)
    }
    return new(big.Int)
}

type BundleHeap []*types.Bundle
func (h *BundleHeap) Len() int { return len(*h) }
func (h *BundleHeap) Less(i, j int) bool { return (*h)[i].Price.Cmp((*h)[j].Price) == -1 }
func (h *BundleHeap) Swap(i, j int) { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }
func (h *BundleHeap) Push(x interface{}) { *h = append(*h, x.(*types.Bundle)) }
func (h *BundleHeap) Pop() interface{} { old := *h; n := len(old); x := old[n-1]; *h = old[0:n-1]; return x }