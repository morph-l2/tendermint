package l2node

import (
	"sync"
	"time"

	"github.com/tendermint/tendermint/libs/log"
)

type BlockData struct {
	Height int64
	Txs    [][]byte
	Meta   []byte
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
				txs, metaData, collectedL1Msgs, err := n.l2Node.RequestBlockData(height)
				if err != nil {
					n.logger.Error("failed to call l2Node.RequestBlockData", "err", err)
					return
				}
				if len(txs) > 0 || collectedL1Msgs {
					n.blockData = &BlockData{
						Height: height,
						Txs:    txs,
						Meta:   metaData,
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
					txs, metaData, collectedL1Msgs, err := n.l2Node.RequestBlockData(height)
					if err != nil {
						n.logger.Error("failed to call l2Node.RequestBlockData", "err", err)
						return
					}
					if len(txs) > 0 || collectedL1Msgs {
						n.blockData = &BlockData{
							Height: height,
							Txs:    txs,
							Meta:   metaData,
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

func (n *Notifier) WaitForBlockData() *BlockData {
	n.wg.Wait()
	return n.blockData
}

func (n *Notifier) CleanBlockData() {
	n.blockData = nil
}
