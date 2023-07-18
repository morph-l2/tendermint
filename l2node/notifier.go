package l2node

import (
	"encoding/hex"
	"fmt"
	"time"
)

type BlockData struct {
	Txs      [][]byte
	L2Config []byte
	ZKConfig []byte
	Root     []byte
}

type Notifier struct {
	txsAvailable chan struct{}
	blockData    *BlockData
	l2Node       L2Node
}

func NewNotifier(l2Node L2Node) *Notifier {
	return &Notifier{
		txsAvailable: make(chan struct{}, 1),
		blockData:    nil,
		l2Node:       l2Node,
	}
}

func (n *Notifier) TxsAvailable() <-chan struct{} {
	return n.txsAvailable
}

func (n *Notifier) RequestBlockData(height int64, createEmptyBlocksInterval time.Duration) {
	if createEmptyBlocksInterval == 0 {
		go func() {
			for {
				txs, l2Config, zkConfig, root, err := n.l2Node.RequestBlockData(height)
				if err != nil {
					fmt.Println("ERROR:", err.Error())
					return
				}
				fmt.Println("============================================================")
				fmt.Println("RequestBlockData")
				fmt.Println(height)
				fmt.Println(hex.EncodeToString(l2Config))
				fmt.Println(hex.EncodeToString(zkConfig))
				fmt.Println("============================================================")
				if len(txs) > 0 {
					n.blockData = &BlockData{
						Txs:      txs,
						L2Config: l2Config,
						ZKConfig: zkConfig,
						Root:     root,
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
			for {
				select {
				case <-timeout:
					return
				default:
					txs, l2Config, zkConfig, root, err := n.l2Node.RequestBlockData(height)
					if err != nil {
						fmt.Println("ERROR:", err.Error())
						return
					}
					fmt.Println("============================================================")
					fmt.Println("RequestBlockData")
					fmt.Println(height)
					fmt.Println(hex.EncodeToString(l2Config))
					fmt.Println(hex.EncodeToString(zkConfig))
					fmt.Println("============================================================")
					if len(txs) > 0 {
						n.blockData = &BlockData{
							Txs:      txs,
							L2Config: l2Config,
							ZKConfig: zkConfig,
							Root:     root,
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

func (n *Notifier) CleanBlockData() {
	n.blockData = nil
}
