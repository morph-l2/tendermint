package l2node

import (
	"sync"
	"time"

	"github.com/tendermint/tendermint/libs/log"
)

type BlockData struct {
	Height   int64
	Txs      [][]byte
	L2Config []byte
	ZKConfig []byte
	Root     []byte
}

type Notifier struct {
	txsAvailable chan struct{}
	blockData    *BlockData
	l2Node       L2Node

	wg     sync.WaitGroup
	logger log.Logger
}

func NewNotifier(l2Node L2Node, logger log.Logger) *Notifier {
	return &Notifier{
		txsAvailable: make(chan struct{}, 1),
		blockData:    nil,
		l2Node:       l2Node,
		wg:           sync.WaitGroup{},
		logger:       logger,
	}
}

func (n *Notifier) TxsAvailable() <-chan struct{} {
	return n.txsAvailable
}

func (n *Notifier) RequestBlockData(height int64, createEmptyBlocksInterval time.Duration) {
	n.wg.Add(1)
	if createEmptyBlocksInterval == 0 {
		go func() {
			defer n.wg.Done()
			for {
				txs, configs, err := n.l2Node.RequestBlockData(height)
				if err != nil {
					n.logger.Error("failed to call l2Node.RequestBlockData", "err", err)
					return
				}
				if len(txs) > 0 {
					n.blockData = &BlockData{
						Height:   height,
						Txs:      txs,
						L2Config: configs.L2Config,
						ZKConfig: configs.ZKConfig,
						Root:     configs.Root,
					}
					n.txsAvailable <- struct{}{}
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()
	} else {
		timeout := time.After(createEmptyBlocksInterval)
		go func() {
			defer n.wg.Done()
			for {
				select {
				case <-timeout:
					return
				default:
					txs, configs, err := n.l2Node.RequestBlockData(height)
					if err != nil {
						n.logger.Error("failed to call l2Node.RequestBlockData", "err", err)
						return
					}
					if len(txs) > 0 {
						n.blockData = &BlockData{
							Height:   height,
							Txs:      txs,
							L2Config: configs.L2Config,
							ZKConfig: configs.ZKConfig,
							Root:     configs.Root,
						}
						n.txsAvailable <- struct{}{}
						return
					}
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()
	}
}

func (n *Notifier) GetBlockData() *BlockData {
	return n.blockData
}

func (n *Notifier) WaitForBlockDataFilledWithTxs() *BlockData {
	n.wg.Wait()
	return n.blockData
}

func (n *Notifier) CleanBlockData() {
	n.blockData = nil
}
