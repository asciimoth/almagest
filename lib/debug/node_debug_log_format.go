package debugnodes

import "github.com/asciimoth/pasta/pasta"

func logPayload(logger pasta.Logger, op string, payload []byte, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debugf("%s bytes=%d payload=%q error=%v", op, len(payload), payload, err)
		return
	}
	logger.Debugf("%s bytes=%d payload=%q", op, len(payload), payload)
}

func logResult(logger pasta.Logger, op string, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debugf("%s error=%v", op, err)
		return
	}
	logger.Debug(op)
}
