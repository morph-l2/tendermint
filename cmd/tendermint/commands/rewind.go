package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tendermint/tendermint/state"
)

var rewindToHeight int64

func init() {
	RewindCmd.Flags().Int64Var(&rewindToHeight, "height", 0,
		"height to which the state is rewound")
}

var RewindCmd = &cobra.Command{
	Use:   "rewind",
	Short: "rewind the tendermint state to a specified height",
	Long: `
A rewind is used to rewind the tendermint status to a past certain block height. It performed to restore the tendermint state after a certain height, and prune the blocks after that height.
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		err := state.RewindTo(rewindToHeight, config)
		if err != nil {
			return fmt.Errorf("failed to rollback state: %w", err)
		}

		fmt.Printf("Finished rewinding the state to height %d ", rewindToHeight)
		return nil
	},
}
