package util

import (
	"math/rand"
	"os"
	"regexp"
	"sync"
	"testing"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/morph-l2/go-ethereum/log"
)

// Fail a test should an error occur
func RequireImpl(t *testing.T, err error, printables ...interface{}) {
	t.Helper()
	if err != nil {
		t.Fatal(Red, printables, err, Clear)
	}
}

func FailImpl(t *testing.T, printables ...interface{}) {
	t.Helper()
	t.Fatal(Red, printables, Clear)
}

func RandomizeSlice(slice []byte) []byte {
	_, err := rand.Read(slice)
	if err != nil {
		panic(err)
	}
	return slice
}

func RandomAddress() common.Address {
	var address common.Address
	RandomizeSlice(address[:])
	return address
}

type LogHandler struct {
	mutex         sync.Mutex
	t             *testing.T
	records       []log.Record
	streamHandler log.Handler
}

func (h *LogHandler) Log(record *log.Record) error {
	if err := h.streamHandler.Log(record); err != nil {
		return err
	}
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.records = append(h.records, *record)
	return nil
}

func (h *LogHandler) WasLogged(pattern string) bool {
	re, err := regexp.Compile(pattern)
	RequireImpl(h.t, err)
	h.mutex.Lock()
	defer h.mutex.Unlock()
	for _, record := range h.records {
		if re.MatchString(record.Msg) {
			return true
		}
	}
	return false
}

func newLogHandler(t *testing.T) *LogHandler {
	return &LogHandler{
		t:             t,
		records:       make([]log.Record, 0),
		streamHandler: log.StreamHandler(os.Stderr, log.TerminalFormat(false)),
	}
}

func InitTestLog(t *testing.T, level log.Lvl) *LogHandler {
	handler := newLogHandler(t)
	glogger := log.NewGlogHandler(handler)
	glogger.Verbosity(level)
	log.Root().SetHandler(glogger)
	return handler
}
