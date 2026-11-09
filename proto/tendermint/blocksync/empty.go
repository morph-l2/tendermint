package blocksync

type Empty struct{}

func (Empty) Marshal() ([]byte, error)                 { return nil, nil }
func (Empty) MarshalTo([]byte) (int, error)            { return 0, nil }
func (Empty) MarshalToSizedBuffer([]byte) (int, error) { return 0, nil }
func (Empty) Size() int                                { return 0 }

func (*Empty) Unmarshal([]byte) error { return nil }
