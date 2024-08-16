package coregrpc

import (
	"context"
)

type broadcastAPI struct {
}

func (bapi *broadcastAPI) Ping(ctx context.Context, req *RequestPing) (*ResponsePing, error) {
	// kvstore so we can check if the server is up
	return &ResponsePing{}, nil
}
